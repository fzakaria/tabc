package main

import (
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
	"github.com/fzakaria/tabc/internal/proc"
	"github.com/fzakaria/tabc/internal/runner"
	"github.com/fzakaria/tabc/internal/signals"
	"github.com/fzakaria/tabc/internal/state"
	"github.com/fzakaria/tabc/internal/supervisor"
)

// defaultBundle is the bundle when --bundle is not given: the current
// directory, as the runtime specification's reference runtime has it.
const defaultBundle = "."

// pidFileMode is the mode of the file an engine reads the container's
// pid from.
const pidFileMode = 0o644

// defaultHookTimeout bounds a hook whose configuration sets no timeout
// of its own.
const defaultHookTimeout = 30 * time.Second

// deleteWait is how long delete waits for a supervisor to go, and
// deletePoll how often it looks.
const (
	deleteWait = 5 * time.Second
	deletePoll = 20 * time.Millisecond
)

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
	process, dir, err := runner.Create(record, loaded.Plan(), runner.Launch{
		Root:          root,
		ID:            id,
		ConsoleSocket: inv.ConsoleSocket,
		Start: func(process *exec.Cmd) error {
			return startInNetwork(process, loaded)
		},
		// The supervisor is the container's process from the engine's
		// side, and the hooks are told its pid: Docker's hook reads the
		// network namespace to attach the container's interface to from
		// that pid.
		Created: func(pid int) error {
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

	// The pipe to the creator, which hears how the supervisor fares.
	ready := proc.Handed()

	// The browser this supervisor starts ends with the supervisor.
	if err := proc.Contain(); err != nil {
		return failSupervise(ready, fmt.Errorf("keeping the browser with the supervisor: %w", err))
	}

	files, err := share()
	if err != nil {
		return failSupervise(ready, err)
	}
	// A container shown in a browser somebody else brings needs none
	// here, so a missing Chromium is only an error once it is wanted.
	browser, _ := chromiumPath()

	return supervisor.Run(supervisor.Options{
		Root:          state.Root(inv.Root),
		ID:            id,
		ConsoleSocket: inv.ConsoleSocket,
		MirrorPorts:   inv.MirrorPorts,
		Ready:         ready,
		Share:         files,
		Chromium:      browser,
	})
}

// failSupervise tells the creator, on the pipe to the creator, why the
// supervisor cannot start.
func failSupervise(ready *os.File, err error) int {
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

		// The supervisor leads a group of its own: the supervisor and
		// the browser it started.
		proc.KillGroup(record.Pid)
	}

	// A container is stopped as soon as its program ends, while its
	// supervisor is still stopping the browser and writing its log. The
	// directory goes once the supervisor has: removing it under the
	// supervisor fails on whatever the supervisor writes next.
	deadline := time.Now().Add(deleteWait)
	for state.Alive(record) && time.Now().Before(deadline) {
		time.Sleep(deletePoll)
	}
	return state.Remove(root, id)
}
