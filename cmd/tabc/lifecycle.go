package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	specs "github.com/opencontainers/runtime-spec/specs-go"

	"github.com/fzakaria/tabc/internal/bundle"
	"github.com/fzakaria/tabc/internal/cli"
	"github.com/fzakaria/tabc/internal/plan"
	"github.com/fzakaria/tabc/internal/signals"
	"github.com/fzakaria/tabc/internal/state"
	"github.com/fzakaria/tabc/internal/supervisor"
)

// readyDescriptor is where a supervisor finds the pipe to its creator:
// the first descriptor after stdin, stdout and stderr.
const readyDescriptor = 3

// defaultBundle is the bundle when --bundle is not given: the current
// directory, as the runtime specification's reference runtime has it.
const defaultBundle = "."

// pidFileMode is the mode of the file an engine reads the container's
// pid from.
const pidFileMode = 0o644

// defaultHookTimeout bounds a hook whose configuration sets no timeout
// of its own.
const defaultHookTimeout = 30 * time.Second

// logTailLines is how much of a supervisor's log a failed create
// repeats.
const logTailLines = 15

// deleteWait is how long delete waits for a killed supervisor to go,
// and deletePoll how often it looks.
const (
	deleteWait = 5 * time.Second
	deletePoll = 20 * time.Millisecond
)

// launch is what starting a supervisor takes.
type launch struct {
	root string
	id   string
	dir  string

	// consoleSocket is the engine's socket for the terminal, or "".
	consoleSocket string

	// stdin is the program's input, nil for this command's own.
	stdin *os.File

	// start starts the supervisor's process in the container's network.
	start func(process *exec.Cmd) error

	// created runs once the supervisor's process exists, before the
	// guest is up: an engine's hooks.
	created func(pid int) error
}

// create makes a container from a bundle: its record, and a supervisor
// that brings the guest up and then waits for start.
func create(inv cli.Invocation) error {
	supervisorProcess, _, err := createFromBundle(inv)
	if err != nil {
		return err
	}

	// The supervisor outlives this command. An engine waits on the pid
	// in the pid file, which is the supervisor's.
	return supervisorProcess.Process.Release()
}

// runBundle is create, start and wait in one command, for running a
// bundle by hand: the supervisor stays this command's child, and its
// status is the program's.
func runBundle(inv cli.Invocation) (int, error) {
	supervisorProcess, dir, err := createFromBundle(inv)
	if err != nil {
		return 0, err
	}
	id, _ := inv.ID()
	return startAndWait(supervisorProcess, state.Root(inv.Root), id, dir)
}

// startAndWait runs a created container's program and waits for the
// program to end. A signal sent to this command meanwhile is passed to
// the program. The container is deleted afterwards.
func startAndWait(supervisorProcess *exec.Cmd, root, id, dir string) (int, error) {
	defer state.Remove(root, id)
	socket := filepath.Join(dir, state.SocketFile)

	if err := supervisor.Start(socket); err != nil {
		supervisorProcess.Process.Kill()
		supervisorProcess.Wait()
		return 0, err
	}

	// The supervisor leads a session of its own, so a signal meant for
	// the container arrives here and has to be carried over.
	caught := make(chan os.Signal, 1)
	signal.Notify(caught, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer signal.Stop(caught)
	go func() {
		for received := range caught {
			number, _ := received.(syscall.Signal)
			name, err := signals.Parse(strconv.Itoa(int(number)))
			if err != nil {
				continue
			}
			supervisor.Kill(socket, name)
		}
	}()

	err := supervisorProcess.Wait()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	return 0, err
}

// createFromBundle makes a container from the bundle the command line
// names and waits until the guest is up. It returns the supervisor and
// the container's state directory.
func createFromBundle(inv cli.Invocation) (*exec.Cmd, string, error) {
	id, err := inv.ID()
	if err != nil {
		return nil, "", err
	}
	root := state.Root(inv.Root)

	bundleDir := inv.Bundle
	if bundleDir == "" {
		bundleDir = defaultBundle
	}
	loaded, err := bundle.Load(bundleDir)
	if err != nil {
		return nil, "", err
	}

	record := state.Container{
		ID:          id,
		Bundle:      loaded.Dir,
		Status:      state.Creating,
		Created:     time.Now().UTC(),
		Annotations: loaded.Spec.Annotations,
	}
	process, dir, err := createContainer(record, loaded.Plan(), launch{
		root:          root,
		id:            id,
		consoleSocket: inv.ConsoleSocket,
		start: func(process *exec.Cmd) error {
			return startInNetwork(process, loaded)
		},
		// The supervisor is the container's process from the engine's
		// side, and the hooks are told its pid: Docker's hook reads the
		// network namespace to attach the container's interface to from
		// that pid.
		created: func(pid int) error {
			record.Pid = pid
			return runCreateHooks(loaded, record)
		},
	})
	if err != nil {
		return nil, "", err
	}

	if inv.PidFile != "" {
		pid := strconv.Itoa(process.Process.Pid)
		if err := os.WriteFile(inv.PidFile, []byte(pid), pidFileMode); err != nil {
			process.Process.Kill()
			state.Remove(root, id)
			return nil, "", err
		}
	}
	return process, dir, nil
}

// createContainer writes a container's record and plan, starts its
// supervisor and waits until the guest is up. A failure leaves nothing
// behind.
func createContainer(record state.Container, planned plan.Plan, l launch) (*exec.Cmd, string, error) {
	dir, err := state.Create(l.root, l.id)
	if err != nil {
		return nil, "", err
	}
	l.dir = dir

	fail := func(err error) (*exec.Cmd, string, error) {
		// What the browser said on its way down goes into the error:
		// the log is about to be removed with the rest.
		tail := logTail(filepath.Join(dir, state.LogFile))
		state.Remove(l.root, l.id)
		if tail != "" {
			err = fmt.Errorf("%w\n%s", err, tail)
		}
		return nil, "", err
	}

	if err := state.Save(dir, record); err != nil {
		return fail(err)
	}
	if err := plan.Save(dir, planned); err != nil {
		return fail(err)
	}
	process, err := startSupervisor(l, &record)
	if err != nil {
		return fail(err)
	}
	return process, dir, nil
}

// logTail returns the last lines of a supervisor's log, or "" when
// there is no log.
func logTail(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) > logTailLines {
		lines = lines[len(lines)-logTailLines:]
	}
	return strings.Join(lines, "\n")
}

// startSupervisor starts the supervisor, runs what the launch wants run
// once the supervisor exists, and waits for the supervisor to say the
// guest is up.
func startSupervisor(l launch, record *state.Container) (*exec.Cmd, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}

	args := []string{commandSupervise, "--root", l.root}
	if l.consoleSocket != "" {
		args = append(args, "--console-socket", l.consoleSocket)
	}
	args = append(args, l.id)

	// The pipe the supervisor reports on.
	readyRead, readyWrite, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer readyRead.Close()

	// The supervisor's stdin, stdout and stderr are the program's:
	// this command's own, which an engine made the container's. A
	// session of its own keeps the supervisor apart from the caller's
	// terminal and process group.
	process := exec.Command(self, args...)
	process.Stdin = os.Stdin
	if l.stdin != nil {
		process.Stdin = l.stdin
	}
	process.Stdout = os.Stdout
	process.Stderr = os.Stderr
	process.ExtraFiles = []*os.File{readyWrite}
	process.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	err = l.start(process)
	readyWrite.Close()
	if err != nil {
		return nil, fmt.Errorf("starting the supervisor: %w", err)
	}

	record.Pid = process.Process.Pid
	if err := state.Save(l.dir, *record); err != nil {
		process.Process.Kill()
		return nil, err
	}
	if l.created != nil {
		if err := l.created(process.Process.Pid); err != nil {
			process.Process.Kill()
			return nil, err
		}
	}

	// One line: ReadyOK, or why there is no container.
	answer, err := bufio.NewReader(readyRead).ReadString('\n')
	answer = strings.TrimSpace(answer)
	if answer == supervisor.ReadyOK {
		return process, nil
	}
	process.Process.Kill()
	if answer == "" {
		return nil, fmt.Errorf("the supervisor exited without a word (%v)", err)
	}
	return nil, errors.New(answer)
}

// runCreateHooks runs the hooks the specification places between a
// container's namespaces existing and its program starting. Each gets
// the container's state on stdin.
func runCreateHooks(loaded *bundle.Bundle, record state.Container) error {
	hooks := loaded.Spec.Hooks
	if hooks == nil {
		return nil
	}

	input, err := json.Marshal(stateOf(record))
	if err != nil {
		return err
	}

	//nolint:staticcheck // Prestart is deprecated in the specification and still what Docker uses.
	all := append(append([]specs.Hook{}, hooks.Prestart...), hooks.CreateRuntime...)
	for _, hook := range all {
		timeout := defaultHookTimeout
		if hook.Timeout != nil && *hook.Timeout > 0 {
			timeout = time.Duration(*hook.Timeout) * time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)

		command := exec.CommandContext(ctx, hook.Path)
		if len(hook.Args) > 0 {
			command.Args = hook.Args
		}
		command.Env = hook.Env
		command.Stdin = bytes.NewReader(input)
		output, err := command.CombinedOutput()
		cancel()
		if err != nil {
			return fmt.Errorf("hook %s: %w: %s", hook.Path, err, strings.TrimSpace(string(output)))
		}
	}
	return nil
}

// supervise is the supervisor's own entry, run by create.
func supervise(inv cli.Invocation) int {
	id, err := inv.ID()
	if err != nil {
		fmt.Fprintf(os.Stderr, "tabc: %v\n", err)
		return supervisor.ExitFailed
	}

	share, err := shareDir()
	if err != nil {
		return failSupervise(err)
	}
	browser, err := chromiumPath()
	if err != nil {
		return failSupervise(err)
	}

	return supervisor.Run(supervisor.Options{
		Root:          state.Root(inv.Root),
		ID:            id,
		ConsoleSocket: inv.ConsoleSocket,
		Ready:         os.NewFile(readyDescriptor, "ready"),
		ShareDir:      share,
		Chromium:      browser,
	})
}

// failSupervise tells the creator why the supervisor cannot start.
func failSupervise(err error) int {
	ready := os.NewFile(readyDescriptor, "ready")
	fmt.Fprintln(ready, err.Error())
	ready.Close()
	return supervisor.ExitFailed
}

// kill sends a signal to a container's program.
func kill(inv cli.Invocation) error {
	id, err := inv.ID()
	if err != nil {
		return err
	}
	root := state.Root(inv.Root)

	var named string
	if len(inv.Args) > 1 {
		named = inv.Args[1]
	}
	signal, err := signals.Parse(named)
	if err != nil {
		return err
	}

	record, err := state.Load(root, id)
	if err != nil {
		return err
	}
	if record.Status == state.Stopped {
		return fmt.Errorf("container %q is not running", id)
	}
	return supervisor.Kill(filepath.Join(state.Dir(root, id), state.SocketFile), signal)
}

// remove deletes a container. One that still runs is deleted only with
// --force, which ends its supervisor and browser first.
func remove(inv cli.Invocation) error {
	id, err := inv.ID()
	if err != nil {
		return err
	}
	root := state.Root(inv.Root)

	record, err := state.Load(root, id)
	if err != nil {
		// Deleting what is not there is what --force asks to ignore.
		if inv.Force {
			return state.Remove(root, id)
		}
		return err
	}

	if record.Status != state.Stopped {
		if !inv.Force {
			return fmt.Errorf("container %q is %s; stop it first, or use --force", id, record.Status)
		}

		// The supervisor leads a session of its own, so its process
		// group is the supervisor and the browser it started.
		syscall.Kill(-record.Pid, syscall.SIGKILL)
		deadline := time.Now().Add(deleteWait)
		for time.Now().Before(deadline) {
			current, err := state.Load(root, id)
			if err != nil || current.Status == state.Stopped {
				break
			}
			time.Sleep(deletePoll)
		}
	}
	return state.Remove(root, id)
}
