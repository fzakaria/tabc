// Package runner starts a container's supervisor and waits for the
// guest to come up. The lifecycle commands an engine calls use it, and
// so does the daemon that stands in for an engine (internal/engine):
// both make a container the same way and differ only in where its
// input and output go.
package runner

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/fzakaria/tabc/internal/cli"
	"github.com/fzakaria/tabc/internal/plan"
	"github.com/fzakaria/tabc/internal/state"
	"github.com/fzakaria/tabc/internal/supervisor"
)

// CommandSupervise is the command the tabc binary runs a supervisor
// under.
const CommandSupervise = "supervise"

// ReadyDescriptor is where a supervisor finds the pipe to its creator:
// the first descriptor after stdin, stdout and stderr.
const ReadyDescriptor = 3

// logTailLines is how much of a supervisor's log a failed create
// repeats.
const logTailLines = 15

// Launch is what starting a supervisor takes.
type Launch struct {
	// Root and ID name the container; Create fills in dir.
	Root string
	ID   string
	dir  string

	// consoleSocket is the engine's socket for the terminal, or "".
	ConsoleSocket string

	// Stdin, Stdout and Stderr are the program's input and output, each
	// nil for this process's own.
	Stdin  *os.File
	Stdout *os.File
	Stderr *os.File

	// Self is the tabc binary to run the supervisor from, empty for the
	// one running now.
	Self string

	// start starts the supervisor's process in the container's network.
	Start func(process *exec.Cmd) error

	// created runs once the supervisor's process exists, before the
	// guest is up: an engine's hooks.
	Created func(pid int) error
}

// Create writes a container's record and plan, starts its
// supervisor and waits until the guest is up. A failure leaves nothing
// behind.
func Create(record state.Container, planned plan.Plan, l Launch) (*exec.Cmd, string, error) {
	dir, err := state.Create(l.Root, l.ID)
	if err != nil {
		return nil, "", err
	}
	l.dir = dir

	fail := func(err error) (*exec.Cmd, string, error) {
		// What the browser said on its way down goes into the error:
		// the log is about to be removed with the rest.
		tail := logTail(filepath.Join(dir, state.LogFile))
		state.Remove(l.Root, l.ID)
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
func startSupervisor(l Launch, record *state.Container) (*exec.Cmd, error) {
	self := l.Self
	if self == "" {
		var err error
		self, err = os.Executable()
		if err != nil {
			return nil, err
		}
	}

	args := []string{CommandSupervise, "--root", l.Root}
	if l.ConsoleSocket != "" {
		args = append(args, "--"+cli.FlagConsoleSocket, l.ConsoleSocket)
	}
	args = append(args, l.ID)

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
	process.Stdin = orFile(l.Stdin, os.Stdin)
	process.Stdout = orFile(l.Stdout, os.Stdout)
	process.Stderr = orFile(l.Stderr, os.Stderr)

	// With a console socket the program's input and output are the pty,
	// and this command's own are the engine's way of reading what
	// create has to say: containerd's shim waits for them to close
	// before it goes on, which they never would in a supervisor's
	// hands. exec.Cmd gives a process the null device for each of the
	// three left unset.
	if l.ConsoleSocket != "" {
		process.Stdin, process.Stdout, process.Stderr = nil, nil, nil
	}
	process.ExtraFiles = []*os.File{readyWrite}
	process.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	err = l.Start(process)
	readyWrite.Close()
	if err != nil {
		return nil, fmt.Errorf("starting the supervisor: %w", err)
	}

	record.Pid = process.Process.Pid
	if err := state.Save(l.dir, *record); err != nil {
		process.Process.Kill()
		return nil, err
	}
	if l.Created != nil {
		if err := l.Created(process.Process.Pid); err != nil {
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

// orFile returns file unless it is nil.
func orFile(file, fallback *os.File) *os.File {
	if file == nil {
		return fallback
	}
	return file
}
