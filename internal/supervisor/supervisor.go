// Package supervisor is the long-lived process behind one container.
// The lifecycle commands an engine runs are each over in a moment, and
// something has to own the browser between them. The supervisor starts
// headless Chromium on a page that boots the guest, serves that page
// everything it asks for, carries the program's input and output, and
// exits with the program's status. To the engine the supervisor is the
// container's process: its pid is the one in the pid file.
package supervisor

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fzakaria/tabc/internal/bundle"
	"github.com/fzakaria/tabc/internal/console"
	"github.com/fzakaria/tabc/internal/rootfs"
	"github.com/fzakaria/tabc/internal/signals"
	"github.com/fzakaria/tabc/internal/state"
)

// ExitFailed is the supervisor's status when the container could not be
// run or was lost, as opposed to the program's own status.
const ExitFailed = 255

// signalStatusBase is added to a signal's number for the status of a
// container a signal ended before its program ran.
const signalStatusBase = 128

// ReadyOK is what the supervisor writes to its creator once the guest
// is up. Anything else on that pipe is the reason creation failed.
const ReadyOK = "ok"

// bootTimeout is how long the browser and the guest get to come up,
// and killGrace how long a program gets to die of SIGKILL in the guest
// before the supervisor ends the container itself.
const (
	bootTimeout = 2 * time.Minute
	killGrace   = 5 * time.Second
)

// loopbackAddress is where the page's server listens: loopback, on a
// port the system picks.
const loopbackAddress = "127.0.0.1:0"

// The guest's memory: what a container gets when its configuration
// sets no limit, and the least and most the guest is given.
const (
	defaultMemoryMiB = 1024
	minMemoryMiB     = 128
	maxMemoryMiB     = 8192
	bytesPerMiB      = 1 << 20
)

// The terminal the program is told it has, and the size of one whose
// engine gave none.
const (
	terminalType = "xterm-256color"
	defaultCols  = 80
	defaultRows  = 24
)

// resizePoll is how often the supervisor looks whether the engine
// changed the terminal's size. A size set on the controlling end raises
// no event on the other.
const resizePoll = 250 * time.Millisecond

// inputChunk is how much input is read at a time.
const inputChunk = 32 << 10

// envBrowserFlags names extra flags for Chromium, separated by spaces.
const envBrowserFlags = "TABC_CHROMIUM_FLAGS"

// Options are what the supervisor is started with.
type Options struct {
	// Root and ID name the container.
	Root string
	ID   string

	// ConsoleSocket is where the engine waits for the terminal, empty
	// when the engine gave none.
	ConsoleSocket string

	// Ready is the pipe to the creator, nil when nobody waits.
	Ready *os.File

	// ShareDir holds the page, the SDK and the guest's init, and
	// Chromium is the browser to run.
	ShareDir string
	Chromium string
}

// Run supervises one container from creation to its program's end and
// returns the status to exit with.
func Run(options Options) int {
	dir := state.Dir(options.Root, options.ID)

	// The supervisor's own stdout and stderr are the program's, so what
	// the supervisor has to say goes to a file.
	logFile, err := os.OpenFile(filepath.Join(dir, state.LogFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return failCreation(options.Ready, err)
	}
	defer logFile.Close()
	logger := log.New(logFile, "", log.LstdFlags|log.Lmicroseconds)

	status, err := run(options, dir, logger, logFile)
	if err != nil {
		logger.Printf("failed: %v", err)
		return failCreation(options.Ready, err)
	}
	return status
}

// failCreation tells the creator why there is no container, if the
// creator still waits, and returns the supervisor's failure status.
func failCreation(ready *os.File, err error) int {
	if ready != nil {
		fmt.Fprintln(ready, err.Error())
		ready.Close()
	}
	return ExitFailed
}

func run(options Options, dir string, logger *log.Logger, logFile *os.File) (int, error) {
	record, err := state.Load(options.Root, options.ID)
	if err != nil {
		return 0, err
	}
	loaded, err := bundle.Load(record.Bundle)
	if err != nil {
		return 0, err
	}

	// A network namespace made for this container starts with loopback
	// down, and the browser reaches the supervisor over loopback.
	if err := raiseLoopback(); err != nil {
		logger.Printf("loopback: %v", err)
	}

	// The program's terminal, if the container has one.
	wantsTerminal, cols, rows := loaded.Terminal()
	var terminal *os.File
	if wantsTerminal {
		terminal, err = openTerminal(options.ConsoleSocket)
		if err != nil {
			return 0, err
		}
		defer terminal.Close()
	}

	// The program's output: the terminal, or the supervisor's own
	// stdout and stderr, which the engine set up.
	var stdout, stderr io.Writer = os.Stdout, os.Stderr
	var stdin io.Reader = os.Stdin
	if terminal != nil {
		stdout, stderr, stdin = terminal, terminal, terminal
	}

	// The page's server.
	files, err := rootfs.Open(loaded.Rootfs)
	if err != nil {
		return 0, err
	}
	defer files.Close()
	token, err := newToken()
	if err != nil {
		return 0, err
	}
	conversation := newSession(stdout, stderr, logger.Printf)
	guestSpec := loaded.GuestSpec(bundle.NetworkOn)
	pages := &server{
		token:    token,
		shareDir: options.ShareDir,
		config: pageConfig{
			Terminal:  wantsTerminal,
			Cols:      orDefault(int(cols), defaultCols),
			Rows:      orDefault(int(rows), defaultRows),
			Term:      terminalType,
			MemoryMiB: memoryMiB(loaded.MemoryLimit()),
			Network:   true,
		},
		spec:     guestSpec,
		files:    files,
		session:  conversation,
		resolver: resolverFor(loaded.ResolvConf()),
	}
	listener, err := net.Listen("tcp4", loopbackAddress)
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	go http.Serve(listener, pages)

	// The control socket, for start and kill.
	started := make(chan struct{})
	killed := make(chan string, 1)
	var startOnce sync.Once
	// Bound by its bare name from the state directory: a socket's
	// address is too short for some state directories' full paths.
	if err := os.Chdir(dir); err != nil {
		return 0, err
	}
	control, err := net.Listen(controlNetwork, state.SocketFile)
	if err != nil {
		return 0, err
	}
	defer control.Close()
	go serveControl(control, func(words []string) error {
		if len(words) == 0 {
			return errors.New("empty request")
		}
		switch words[0] {
		case requestStart:
			startOnce.Do(func() { close(started) })
			return nil
		case requestKill:
			if len(words) != 2 {
				return errors.New("kill needs a signal")
			}
			select {
			case killed <- words[1]:
			default:
			}
			return nil
		default:
			return fmt.Errorf("unknown request %q", words[0])
		}
	})

	// The browser, with a profile that goes when the container does.
	// The profile is in the record too, so a delete of a container whose
	// supervisor was killed still removes it.
	profile, err := makeProfile(dir)
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(profile)
	record.Profile = profile
	if err := state.Save(dir, record); err != nil {
		return 0, err
	}
	browser, err := startBrowser(options.Chromium, profile, pages.pageURL(listener),
		strings.Fields(os.Getenv(envBrowserFlags)), logFile)
	if err != nil {
		return 0, err
	}
	browserGone := make(chan error, 1)
	go func() { browserGone <- browser.Wait() }()
	defer stopBrowser(browser, browserGone)

	// The supervisor's own end: an engine that gives up on a container
	// signals its process.
	interrupted := make(chan os.Signal, 1)
	signal.Notify(interrupted, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)

	// Created: the guest is up and the image is mounted.
	select {
	case <-conversation.ready:
	case message := <-conversation.failed:
		return 0, fmt.Errorf("the page failed: %s", message)
	case err := <-browserGone:
		return 0, fmt.Errorf("the browser exited before the guest was up (%v); see %s", err, logFile.Name())
	case <-time.After(bootTimeout):
		return 0, fmt.Errorf("the guest was not up after %s; see %s", bootTimeout, logFile.Name())
	case caught := <-interrupted:
		return 0, fmt.Errorf("stopped by %s while the guest was starting", caught)
	}
	record.Status = state.Created
	if err := state.Save(dir, record); err != nil {
		return 0, err
	}
	if options.Ready != nil {
		fmt.Fprintln(options.Ready, ReadyOK)
		options.Ready.Close()
	}
	logger.Printf("created")

	// Running, once the engine says start. A kill before that ends the
	// container without the program ever running.
	select {
	case <-started:
	case name := <-killed:
		logger.Printf("killed by %s before start", name)
		return finish(dir, record, logger, signalStatusBase+signals.Number(name)), nil
	case caught := <-interrupted:
		logger.Printf("stopped by %s before start", caught)
		return finish(dir, record, logger, ExitFailed), nil
	case err := <-browserGone:
		logger.Printf("the browser exited before start: %v", err)
		return finish(dir, record, logger, ExitFailed), nil
	}
	if err := conversation.send(pageMessage{Type: toPageStart}); err != nil {
		logger.Printf("start: %v", err)
		return finish(dir, record, logger, ExitFailed), nil
	}
	record.Status = state.Running
	if err := state.Save(dir, record); err != nil {
		logger.Printf("state: %v", err)
	}
	logger.Printf("running")

	go pumpInput(stdin, conversation, terminal != nil, logger)
	if terminal != nil {
		go watchSize(terminal, conversation)
	}

	// Until the program ends: signals go to the program, and anything
	// that takes the page away ends the container.
	var forced <-chan time.Time
	for {
		select {
		case status := <-conversation.exit:
			logger.Printf("exited with %d", status)
			return finish(dir, record, logger, status), nil

		case name := <-killed:
			logger.Printf("kill %s", name)
			if err := conversation.send(pageMessage{Type: toPageKill, Signal: name}); err != nil {
				logger.Printf("kill: %v", err)
			}
			// SIGKILL cannot be refused by the program, so a guest that
			// does not report the death is a guest that is stuck.
			if name == "KILL" && forced == nil {
				forced = time.After(killGrace)
			}

		case <-forced:
			logger.Printf("the guest did not report the kill; ending the container")
			return finish(dir, record, logger, signalStatusBase+signals.Number("KILL")), nil

		case message := <-conversation.failed:
			fmt.Fprintf(stderr, "tabc: the page failed: %s\n", message)
			logger.Printf("the page failed: %s", message)
			return finish(dir, record, logger, ExitFailed), nil

		case <-conversation.gone:
			// The page may have sent the status just before it went.
			select {
			case status := <-conversation.exit:
				return finish(dir, record, logger, status), nil
			default:
			}
			logger.Printf("the page went away")
			return finish(dir, record, logger, ExitFailed), nil

		case err := <-browserGone:
			logger.Printf("the browser exited: %v", err)
			return finish(dir, record, logger, ExitFailed), nil

		case caught := <-interrupted:
			logger.Printf("stopped by %s", caught)
			return finish(dir, record, logger, ExitFailed), nil
		}
	}
}

// finish records the container as stopped and returns its status.
func finish(dir string, record state.Container, logger *log.Logger, status int) int {
	record.Status = state.Stopped
	if err := state.Save(dir, record); err != nil {
		logger.Printf("state: %v", err)
	}
	return status
}

// openTerminal makes the program's terminal on the host side and hands
// its controlling end to the engine.
func openTerminal(socket string) (*os.File, error) {
	if socket == "" {
		return nil, errors.New("the container wants a terminal and the engine gave no console socket")
	}

	controller, terminal, err := console.Open()
	if err != nil {
		return nil, fmt.Errorf("opening a pty: %w", err)
	}
	defer controller.Close()

	if err := console.MakeRaw(terminal); err != nil {
		terminal.Close()
		return nil, err
	}
	if err := console.Send(socket, controller); err != nil {
		terminal.Close()
		return nil, err
	}
	return terminal, nil
}

// pumpInput carries the program's input to the page until the input
// ends. On pipes the end is passed on, so a program reading to the end
// of its input stops.
func pumpInput(input io.Reader, conversation *session, isTerminal bool, logger *log.Logger) {
	buffer := make([]byte, inputChunk)
	for {
		count, err := input.Read(buffer)
		if count > 0 {
			if err := conversation.sendInput(buffer[:count]); err != nil {
				return
			}
		}
		if err == nil {
			continue
		}
		if !errors.Is(err, io.EOF) {
			logger.Printf("input: %v", err)
		}
		if !isTerminal {
			conversation.send(pageMessage{Type: toPageEOF})
		}
		return
	}
}

// watchSize tells the page whenever the engine has resized the
// terminal.
func watchSize(terminal *os.File, conversation *session) {
	var lastCols, lastRows int
	for {
		select {
		case <-conversation.gone:
			return
		case <-time.After(resizePoll):
		}

		cols, rows, err := console.Size(terminal)
		if err != nil {
			return
		}
		if cols == 0 || rows == 0 || (cols == lastCols && rows == lastRows) {
			continue
		}
		lastCols, lastRows = cols, rows
		conversation.send(pageMessage{Type: toPageResize, Cols: cols, Rows: rows})
	}
}

// stopBrowser ends the browser and everything it started, and waits for
// the browser itself.
func stopBrowser(browser *exec.Cmd, gone <-chan error) {
	browser.Process.Kill()
	select {
	case <-gone:
	case <-time.After(killGrace):
	}
}

// memoryMiB returns the guest's memory for a container's limit in
// bytes, 0 meaning none was set.
func memoryMiB(limit int64) int {
	if limit <= 0 {
		return defaultMemoryMiB
	}
	return int(min(max(limit/bytesPerMiB, minMemoryMiB), maxMemoryMiB))
}

// orDefault returns value unless it is zero.
func orDefault(value, fallback int) int {
	if value == 0 {
		return fallback
	}
	return value
}
