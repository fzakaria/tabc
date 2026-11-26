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
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fzakaria/tabc/internal/console"
	"github.com/fzakaria/tabc/internal/guest"
	"github.com/fzakaria/tabc/internal/netreg"
	"github.com/fzakaria/tabc/internal/plan"
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

// openTimeout is how long a browser that a person has to bring gets to
// load the page: long enough to find the tab.
const openTimeout = 10 * time.Minute

// loopbackAddress is where the page's server listens: loopback, on a
// port the system picks. loopbackHost is where a published port
// listens when its flag names no address.
const (
	loopbackHost    = "127.0.0.1"
	loopbackAddress = loopbackHost + ":0"
)

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

// volumeMountPattern is where in the guest the page mounts a volume, by
// the volume's number.
const volumeMountPattern = "/tabc/vol/%d"

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

	// MirrorPorts has the supervisor listen on every port the
	// container's program opens. That is right when the supervisor runs
	// in a network namespace that is the container's own, and wrong on
	// the host's network, where the ports would be the host's.
	MirrorPorts bool

	// Share holds the page, the SDK and the guest's init
	// (internal/assets), and Chromium is the browser to run.
	Share    fs.FS
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
	planned, err := plan.Load(dir)
	if err != nil {
		return 0, fmt.Errorf("the container's plan: %w", err)
	}
	logger.Printf("supervising %s", options.ID)

	// A network namespace made for this container starts with loopback
	// down, and the browser reaches the supervisor over loopback.
	if err := raiseLoopback(); err != nil {
		logger.Printf("loopback: %v", err)
	}

	// The program's input and output: the supervisor's own stdin,
	// stdout and stderr, which whoever started the container set up, or
	// a pty whose controlling end goes to the engine.
	var stdout, stderr io.Writer = os.Stdout, os.Stderr
	var stdin io.Reader = os.Stdin
	var terminal *os.File
	switch planned.Terminal {
	case plan.TerminalConsole:
		terminal, err = openTerminal(options.ConsoleSocket)
		if err != nil {
			return 0, err
		}
		defer terminal.Close()
		stdout, stderr, stdin = terminal, terminal, terminal
	case plan.TerminalInherit:
		terminal = os.Stdin
	}
	hasTerminal := planned.Terminal != plan.TerminalNone

	// The page's server.
	tree, err := openTree(planned)
	if err != nil {
		return 0, err
	}
	defer tree.Close()
	token, err := newToken()
	if err != nil {
		return 0, err
	}

	// The volumes: each a directory of the host the page mounts in the
	// guest under a number, and init binds to its place in the
	// container.
	guestSpec := planned.Spec
	var volumes []fileServer
	// Never nil: the page is sent this as JSON, where a nil list is
	// null and not a list with nothing in it.
	mounted := []pageVolume{}
	for index, volume := range planned.Volumes {
		directory, err := rootfs.OpenDir(volume.Source)
		if err != nil {
			return 0, fmt.Errorf("volume %s: %w", volume.Source, err)
		}
		defer directory.Close()

		inGuest := fmt.Sprintf(volumeMountPattern, index)
		volumes = append(volumes, rootfs.Handler{Tree: directory, Writable: !volume.ReadOnly})
		mounted = append(mounted, pageVolume{Path: inGuest, ReadOnly: volume.ReadOnly})
		guestSpec.Volumes = append(guestSpec.Volumes, guest.Volume{
			Source:   inGuest,
			Target:   volume.Target,
			ReadOnly: volume.ReadOnly,
		})
	}
	conversation := newSession(stdout, stderr, logger.Printf)

	// Connections into the container: every port the program opens,
	// when the container has a network of its own to open them on.
	incoming := newInbound(conversation, logger.Printf)
	defer incoming.close()
	if options.MirrorPorts {
		conversation.onListen = func(port int, open bool) {
			if !open {
				incoming.unlisten(port)
				return
			}
			if err := incoming.listen(anyAddress, port, port); err != nil {
				logger.Printf("mirroring port %d: %v", port, err)
			}
		}
	}
	// Second programs, which `tabc exec` asks for once the container's
	// own is running.
	second := newExecs(conversation, guestSpec, terminalType, logger.Printf)
	pages := &server{
		token: token,
		share: options.Share,
		config: pageConfig{
			Terminal:    hasTerminal,
			Cols:        orDefault(planned.Cols, defaultCols),
			Rows:        orDefault(planned.Rows, defaultRows),
			Term:        terminalType,
			Title:       orTitle(planned.Title, options.ID),
			MemoryMiB:   memoryMiB(planned.MemoryBytes),
			Network:     true,
			MirrorPorts: options.MirrorPorts,
			Volumes:     mounted,
		},
		spec:     guestSpec,
		volumes:  volumes,
		files:    rootfs.Handler{Tree: tree},
		session:  conversation,
		inbound:  incoming,
		execs:    second,
		resolver: resolverFor(planned.ResolvConfPath),
		root:     options.Root,
		networks: networkNames(planned.Networks),
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
		case requestResize:
			// A terminal's size from whoever holds its other end, for a
			// terminal that is not a pty the supervisor could ask.
			if len(words) != 3 {
				return errors.New("resize needs columns and rows")
			}
			cols, colsErr := strconv.Atoi(words[1])
			rows, rowsErr := strconv.Atoi(words[2])
			if colsErr != nil || rowsErr != nil || cols <= 0 || rows <= 0 {
				return errors.New("resize needs two positive numbers")
			}
			return conversation.send(pageMessage{Type: toPageResize, Cols: cols, Rows: rows})
		default:
			return fmt.Errorf("unknown request %q", words[0])
		}
	}, map[string]takeover{requestExec: second.handle})

	// The browser: a headless one of the container's own, or whichever
	// browser is going to be shown the page.
	browserGone := make(chan error, 1)
	if planned.Browser == plan.BrowserHeadless {
		// A profile that goes when the container does. The profile is in
		// the record too, so a delete of a container whose supervisor was
		// killed still removes it.
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
		logger.Printf("browser started")
		go func() { browserGone <- browser.Wait() }()
		defer stopBrowser(browser, browserGone)
	} else {
		// The page's address goes in the record, for whoever loads it,
		// and to the terminal, for a person to click.
		record.URL = pages.pageURL(listener)
		if err := state.Save(dir, record); err != nil {
			return 0, err
		}
		logger.Printf("waiting for a browser at %s", record.URL)
		// Lines end with a carriage return as well as a newline: with a
		// terminal the user's own is in raw mode by now, where a newline
		// alone moves down and leaves the cursor in its column, and the
		// program's first prompt would start halfway across the screen.
		switch planned.Browser {
		case plan.BrowserOpen:
			// Said as what happened, not as something to do: a person who
			// opens the address again gets a second page, which can only
			// say that the first one is the container.
			if err := OpenInBrowser(record.URL); err != nil {
				fmt.Fprintf(os.Stderr, "tabc: could not open a browser (%v). Open this page to start the container:\r\n  %s\r\n", err, record.URL)
			} else {
				fmt.Fprintf(os.Stderr, "tabc: the container is running in a tab that just opened in your browser\r\n")
			}
		case plan.BrowserLink:
			fmt.Fprintf(os.Stderr, "tabc: open this page to start the container:\r\n  %s\r\n", record.URL)
		}
	}

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
	case <-time.After(bootTimeoutFor(planned.Browser)):
		return 0, fmt.Errorf("the guest was not up after %s; see %s", bootTimeoutFor(planned.Browser), logFile.Name())
	case caught := <-interrupted:
		return 0, fmt.Errorf("stopped by %s while the guest was starting", caught)
	}
	// The ports published to the host, now that there is a guest to
	// carry their connections to. A port that cannot be listened on is
	// a container that cannot be created: its user asked for the port.
	for _, published := range planned.Publish {
		hostIP := published.HostIP
		if hostIP == "" {
			hostIP = loopbackHost
		}
		if err := incoming.listen(hostIP, published.HostPort, published.GuestPort); err != nil {
			return 0, fmt.Errorf("publishing port %d: %w", published.HostPort, err)
		}
	}

	// The container's names on its tabc networks, now that connections
	// for them have somewhere to go. The files are in the record too, so
	// a container whose supervisor was killed still leaves its networks.
	relay := strings.Replace(pages.pageURL(listener), "http://", "ws://", 1) + routeRelay
	for _, network := range planned.Networks {
		files, address, err := netreg.Register(options.Root, network.Name, options.ID, relay, network.Names)
		record.Registrations = append(record.Registrations, files...)
		if err != nil {
			netreg.Unregister(record.Registrations)
			return 0, fmt.Errorf("joining network %s: %w", network.Name, err)
		}
		logger.Printf("on network %s as %s at %s", network.Name, strings.Join(network.Names, ", "), address)
	}
	defer netreg.Unregister(record.Registrations)

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
	second.running.Store(true)
	logger.Printf("running")

	go pumpInput(stdin, conversation, hasTerminal, logger)
	if hasTerminal {
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

// openTree opens the container's root file system: an image's layers,
// or a directory.
func openTree(planned plan.Plan) (rootfs.Tree, error) {
	if len(planned.Layers) > 0 {
		return rootfs.OpenLayers(planned.Layers)
	}
	return rootfs.OpenDir(planned.Rootfs)
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

// bootTimeoutFor returns how long the guest gets to come up, which is
// longer when a person has to open the page than when the supervisor's
// own browser does.
func bootTimeoutFor(browser plan.Browser) time.Duration {
	if browser == plan.BrowserHeadless {
		return bootTimeout
	}
	return openTimeout
}

// orTitle returns the container's title, or its id when the plan gives
// none.
func orTitle(title, id string) string {
	if title == "" {
		return id
	}
	return title
}

// networkNames returns the names of the networks a plan puts the
// container on.
func networkNames(networks []plan.Network) []string {
	names := make([]string, 0, len(networks))
	for _, network := range networks {
		names = append(names, network.Name)
	}
	return names
}
