package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	specs "github.com/opencontainers/runtime-spec/specs-go"
	"golang.org/x/term"

	"github.com/fzakaria/tabc/internal/cli"
	"github.com/fzakaria/tabc/internal/console"
	"github.com/fzakaria/tabc/internal/proc"
	"github.com/fzakaria/tabc/internal/runner"
	"github.com/fzakaria/tabc/internal/signals"
	"github.com/fzakaria/tabc/internal/state"
	"github.com/fzakaria/tabc/internal/supervisor"
)

// exec runs a second program in a running container:
//
//	tabc exec [-it] [-e NAME=value] [-w dir] [-u user[:group]] <id or name> <program> [arguments]
//
// and, as an engine calls it, with the program described in a file and
// the command itself gone once the program is running:
//
//	tabc exec --process <file> --pid-file <file> --detach [--console-socket <socket>] <id>
//
// Either way a process on the host stands for the program: this command,
// or with --detach a child of it that stays. The process carries the
// program's input and output and exits with the program's status, which
// is what an engine waits for.

// commandExec is the command.
const commandExec = "exec"

// envExecChild marks the process that stays after `exec --detach`.
const envExecChild = "TABC_EXEC_CHILD"

// execFlags are exec's flags that take a value, by every name each goes
// under. Any other flag is a switch. An engine passes flags tabc has no
// use for, and knowing which of them take a value is what keeps the
// value from being read as the container's id.
var execValueFlags = map[string]string{
	"process":         flagExecProcess,
	"p":               flagExecProcess,
	"console-socket":  cli.FlagConsoleSocket,
	"pid-file":        flagExecPidFile,
	"env":             flagExecEnv,
	"e":               flagExecEnv,
	"cwd":             flagExecCwd,
	"workdir":         flagExecCwd,
	"w":               flagExecCwd,
	"user":            flagExecUser,
	"u":               flagExecUser,
	"cap":             flagExecIgnored,
	"c":               flagExecIgnored,
	"apparmor":        flagExecIgnored,
	"process-label":   flagExecIgnored,
	"cgroup":          flagExecIgnored,
	"preserve-fds":    flagExecIgnored,
	"additional-gids": flagExecIgnored,
	"g":               flagExecIgnored,
}

// The flags exec acts on, each under one name.
const (
	flagExecProcess     = "process"
	flagExecPidFile     = "pid-file"
	flagExecEnv         = "env"
	flagExecCwd         = "cwd"
	flagExecUser        = "user"
	flagExecIgnored     = ""
	flagExecTerminal    = "tty"
	flagExecInteractive = "interactive"
	flagExecDetach      = "detach"
)

// execShortSwitches are the one-letter switches, which may be written
// together: -it.
var execShortSwitches = map[rune]string{
	't': flagExecTerminal,
	'i': flagExecInteractive,
	'd': flagExecDetach,
}

// execOptions is exec's command line.
type execOptions struct {
	id      string
	command []string

	// process is a file holding the program as the runtime
	// specification describes one, in place of command.
	process string

	env  []string
	cwd  string
	user string

	terminal    bool
	interactive bool
	detach      bool

	consoleSocket string
	pidFile       string
}

// parseExec reads the words after `exec`: flags up to the container's
// id, then the program and its arguments, whose flags are the
// program's.
func parseExec(args []string) (execOptions, error) {
	var options execOptions

	index := 0
	for ; index < len(args); index++ {
		arg := args[index]
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			break
		}

		// A value flag: "--name value", "-n value" or "--name=value".
		name, value, inline := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if flag, takesValue := execValueFlags[name]; takesValue {
			if !inline {
				if index+1 >= len(args) {
					return options, fmt.Errorf("flag %s needs a value", arg)
				}
				index++
				value = args[index]
			}
			switch flag {
			case flagExecProcess:
				options.process = value
			case cli.FlagConsoleSocket:
				options.consoleSocket = value
			case flagExecPidFile:
				options.pidFile = value
			case flagExecEnv:
				options.env = append(options.env, value)
			case flagExecCwd:
				options.cwd = value
			case flagExecUser:
				options.user = value
			}
			continue
		}

		// A switch by its long name, or one-letter switches together.
		// A switch tabc has no use for is skipped.
		switches := []string{name}
		if !strings.HasPrefix(arg, "--") {
			switches = switches[:0]
			for _, letter := range name {
				switches = append(switches, execShortSwitches[letter])
			}
		}
		for _, flag := range switches {
			switch flag {
			case flagExecTerminal:
				options.terminal = true
			case flagExecInteractive:
				options.interactive = true
			case flagExecDetach:
				options.detach = true
			}
		}
	}

	if index >= len(args) {
		return options, errors.New("exec: a container id is required")
	}
	options.id = args[index]
	options.command = args[index+1:]
	if len(options.command) == 0 {
		options.command = nil
	}
	if options.process == "" && options.command == nil {
		return options, errors.New("exec: a program is required")
	}
	return options, nil
}

// execRequest returns the request the options describe, without a
// terminal's size.
func execRequest(options execOptions) (supervisor.ExecRequest, error) {
	// An engine's file names everything about the program.
	if options.process != "" {
		var process specs.Process
		data, err := os.ReadFile(options.process)
		if err != nil {
			return supervisor.ExecRequest{}, err
		}
		if err := json.Unmarshal(data, &process); err != nil {
			return supervisor.ExecRequest{}, fmt.Errorf("%s: %w", options.process, err)
		}
		return supervisor.ExecRequest{
			Args:        process.Args,
			Env:         process.Env,
			Environment: supervisor.EnvironmentReplace,
			Cwd:         process.Cwd,
			User: &supervisor.ExecUser{
				UID:            process.User.UID,
				GID:            process.User.GID,
				AdditionalGids: process.User.AdditionalGids,
			},
			Terminal: process.Terminal,
		}, nil
	}

	request := supervisor.ExecRequest{
		Args:     options.command,
		Env:      options.env,
		Cwd:      options.cwd,
		UserName: options.user,
		Terminal: options.terminal,
	}
	if options.terminal {
		request.Env = append([]string{envTerm}, request.Env...)
	}
	return request, nil
}

// execCommand is the exec command. It returns the status to exit with.
func execCommand(inv cli.Invocation, args []string) (int, error) {
	options, err := parseExec(args)
	if err != nil {
		return 0, err
	}
	root := state.Root(inv.Root)
	// The container by its id, or by the name `tabc run` gave it. What
	// follows goes by the id the record has.
	record, err := state.Find(root, options.id)
	if err != nil {
		return 0, err
	}
	options.id = record.ID
	if record.Status != state.Running {
		return 0, fmt.Errorf("container %q is %s, not running", options.id, record.Status)
	}
	socket := filepath.Join(state.Dir(root, options.id), state.SocketFile)

	// With --detach the program's process on the host is a child that
	// stays, and this command is done once the program is running.
	isChild := os.Getenv(envExecChild) != ""
	if options.detach && !isChild {
		return 0, detachExec(options)
	}

	var ready *os.File
	if isChild {
		ready = proc.Handed()
	}
	if options.pidFile != "" && !isChild {
		pid := strconv.Itoa(os.Getpid())
		if err := os.WriteFile(options.pidFile, []byte(pid), pidFileMode); err != nil {
			return 0, err
		}
	}
	return attachExec(socket, options, ready)
}

// detachExec starts the process that stays with the program, waits
// until the program is running, and names the process in the pid file.
func detachExec(options execOptions) error {
	self, err := runner.Self()
	if err != nil {
		return err
	}
	readyRead, readyWrite, err := os.Pipe()
	if err != nil {
		return err
	}
	defer readyRead.Close()

	// The same command line again, marked as the one that stays. With a
	// console socket the program's input and output are the pty, and
	// this command's own are left with this command: containerd's shim
	// waits for them to close, which they never would in the hands of a
	// process that stays.
	child := exec.Command(self, os.Args[1:]...)
	child.Env = append(os.Environ(), envExecChild+"=1")
	if options.consoleSocket == "" {
		child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	}
	proc.Lead(child, proc.KeepTerminal)
	if err := proc.Hand(child, readyWrite); err != nil {
		readyWrite.Close()
		return err
	}
	err = child.Start()
	readyWrite.Close()
	if err != nil {
		return err
	}

	// One line: ReadyOK, or why the program is not running.
	answer, err := bufio.NewReader(readyRead).ReadString('\n')
	answer = strings.TrimSpace(answer)
	if answer != supervisor.ReadyOK {
		child.Process.Kill()
		if answer == "" {
			return fmt.Errorf("exec ended without a word (%v)", err)
		}
		return errors.New(answer)
	}

	if options.pidFile != "" {
		pid := strconv.Itoa(child.Process.Pid)
		if err := os.WriteFile(options.pidFile, []byte(pid), pidFileMode); err != nil {
			child.Process.Kill()
			return err
		}
	}
	return child.Process.Release()
}

// attachExec runs the program with this process as its stand-in: the
// program's input and output are this process's, or a pty handed to an
// engine, and the returned status is the program's. ready, if there is
// one, is told once the program is running, or why it is not.
func attachExec(socket string, options execOptions, ready *os.File) (int, error) {
	fail := func(err error) (int, error) {
		if ready != nil {
			fmt.Fprintln(ready, err.Error())
			ready.Close()
		}
		return 0, err
	}

	request, err := execRequest(options)
	if err != nil {
		return fail(err)
	}

	// Where the program's input comes from and its output goes, and how
	// the size of its terminal is learned.
	var input io.Reader = os.Stdin
	var output, errOutput io.Writer = os.Stdout, os.Stderr
	var sizeOf func() (cols, rows int, err error)
	switch {
	case options.consoleSocket != "":
		// An engine's terminal: a pty whose controlling end goes to the
		// engine, as create makes one.
		controller, terminal, err := console.Open()
		if err != nil {
			return fail(fmt.Errorf("opening a pty: %w", err))
		}
		defer terminal.Close()
		if err := console.MakeRaw(terminal); err != nil {
			controller.Close()
			return fail(err)
		}
		err = console.Send(options.consoleSocket, controller)
		controller.Close()
		if err != nil {
			return fail(err)
		}
		input, output, errOutput = terminal, terminal, terminal
		request.Terminal = true
		sizeOf = func() (int, int, error) { return console.Size(terminal) }

	case request.Terminal:
		// The terminal tabc was started on, raw for as long as the
		// program runs: the guest's terminal does the line editing and
		// the echo.
		stdin := int(os.Stdin.Fd())
		if !term.IsTerminal(stdin) {
			return fail(errors.New("-t needs a terminal, and the input is not one"))
		}
		saved, err := term.MakeRaw(stdin)
		if err != nil {
			return fail(err)
		}
		defer term.Restore(stdin, saved)
		defer actOnEscapes()()
		sizeOf = terminalSize
	}
	if sizeOf != nil {
		request.Cols, request.Rows, _ = sizeOf()
	}

	session, err := supervisor.StartExec(socket, request)
	if err != nil {
		return fail(err)
	}
	defer session.Close()
	if ready != nil {
		fmt.Fprintln(ready, supervisor.ReadyOK)
		ready.Close()
	}

	// Input. An engine's call always has one, a pipe or the null
	// device. By hand the program has input only with -i or -t, as with
	// every engine. On pipes the end of input is passed on.
	hasInput := options.process != "" || options.interactive || request.Terminal
	go func() {
		if hasInput {
			io.Copy(session, input)
		}
		if !request.Terminal {
			session.CloseInput()
		}
	}()

	// A terminal's size, whenever it changes.
	if sizeOf != nil {
		go watchExecSize(session, sizeOf, request.Cols, request.Rows)
	}

	// A signal sent to this process is meant for the program.
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
			session.Kill(name)
		}
	}()

	return session.Wait(output, errOutput)
}

// execSizePoll is how often a terminal is asked its size. A size set on
// a pty's controlling end raises no event on the other, so the size is
// asked for and not waited on.
const execSizePoll = 250 * time.Millisecond

// watchExecSize tells the program whenever its terminal's size is no
// longer the last one told, until the session ends.
func watchExecSize(session *supervisor.ExecSession, sizeOf func() (int, int, error), cols, rows int) {
	for {
		time.Sleep(execSizePoll)

		nowCols, nowRows, err := sizeOf()
		if err != nil {
			return
		}
		if nowCols == 0 || nowRows == 0 || (nowCols == cols && nowRows == rows) {
			continue
		}
		cols, rows = nowCols, nowRows
		if err := session.Resize(cols, rows); err != nil {
			return
		}
	}
}
