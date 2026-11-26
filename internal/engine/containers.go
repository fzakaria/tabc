package engine

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fzakaria/tabc/internal/image"
	"github.com/fzakaria/tabc/internal/plan"
	"github.com/fzakaria/tabc/internal/rootfs"
	"github.com/fzakaria/tabc/internal/runner"
	"github.com/fzakaria/tabc/internal/signals"
	"github.com/fzakaria/tabc/internal/state"
	"github.com/fzakaria/tabc/internal/supervisor"
)

// Status is where a container is in its life, in Docker's words.
type Status string

const (
	StatusCreated Status = "created"
	StatusRunning Status = "running"
	StatusExited  Status = "exited"
)

// envTerm tells a program on a terminal what the terminal is.
const envTerm = "TERM=xterm-256color"

// The signals stop sends, how long a container gets after the second
// one before its supervisor is killed outright, and the status of a
// container whose supervisor was.
const (
	signalStop     = "TERM"
	signalKill     = "KILL"
	killWait       = 5 * time.Second
	exitKilled     = 137
	inputChunkSize = 32 << 10
)

// maxBoundFile is the largest single file a mount may name for its
// content to be copied into the container.
const maxBoundFile = 1 << 20

// Mount is a file or directory from outside put inside a container.
type Mount struct {
	// Source is a path on the host, or a volume's name.
	Source string
	// Target is the path in the container.
	Target string
	// ReadOnly keeps the container from changing the source.
	ReadOnly bool
}

// Spec is a container as its creator described it.
type Spec struct {
	Name  string
	Image string

	// Cmd replaces the image's default arguments when not empty, and
	// Entrypoint the image's entry point when ReplaceEntrypoint is set.
	Cmd               []string
	Entrypoint        []string
	ReplaceEntrypoint bool

	Env        []string
	WorkingDir string
	User       string
	Labels     map[string]string

	// Tty gives the program a terminal. OpenStdin keeps its input open
	// for whoever attaches, and StdinOnce closes that input when the
	// first one to attach is done.
	Tty       bool
	OpenStdin bool
	StdinOnce bool

	MemoryBytes int64
	// CPUs is how many CPUs the container asked for, 0 for no limit.
	CPUs       int
	AutoRemove bool
	Publish    []plan.Publish
	Mounts     []Mount

	// Networks are the networks the container is on, each with the
	// extra names the container goes by there.
	Networks map[string][]string
}

// Container is one container and what has become of it.
type Container struct {
	ID      string
	Spec    Spec
	Created time.Time
	Log     *Log

	mu         sync.Mutex
	status     Status
	exitCode   int
	failure    string
	pid        int
	startedAt  time.Time
	finishedAt time.Time
	stdin      *os.File
	// started closes when the run in progress, or the next one, has a
	// program to write input to; done closes when that run ends.
	started chan struct{}
	done    chan struct{}
	removed chan struct{}
}

// State is a container's condition at one moment.
type State struct {
	Status     Status
	ExitCode   int
	Error      string
	Pid        int
	StartedAt  time.Time
	FinishedAt time.Time
}

// State returns the container's condition now.
func (c *Container) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return State{
		Status:     c.status,
		ExitCode:   c.exitCode,
		Error:      c.failure,
		Pid:        c.pid,
		StartedAt:  c.startedAt,
		FinishedAt: c.finishedAt,
	}
}

// Done returns a channel that closes when the run in progress, or the
// next one if none is, ends.
func (c *Container) Done() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.done
}

// Removed returns a channel that closes when the container is removed.
func (c *Container) Removed() <-chan struct{} {
	return c.removed
}

// ShortID is the start of the container's id, which names its state
// directory.
func (c *Container) ShortID() string {
	return c.ID[:shortID]
}

// Create records a container. Nothing runs until Start.
func (e *Engine) Create(spec Spec) (*Container, error) {
	if _, err := e.Image(spec.Image); err != nil {
		return nil, err
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	id := newID()
	if spec.Name == "" {
		spec.Name = "tab-" + id[:shortID]
	}
	for _, existing := range e.containers {
		if existing.Spec.Name == spec.Name {
			return nil, fmt.Errorf("%w: the container name %q is already in use by %s", ErrConflict, spec.Name, existing.ID[:shortID])
		}
	}
	for network := range spec.Networks {
		if _, has := e.networks[network]; !has {
			return nil, fmt.Errorf("%w: network %s not found", ErrNotFound, network)
		}
	}

	container := &Container{
		ID:      id,
		Spec:    spec,
		Created: time.Now(),
		Log:     newLog(),
		status:  StatusCreated,
		started: make(chan struct{}),
		done:    make(chan struct{}),
		removed: make(chan struct{}),
	}
	e.containers[id] = container
	e.events.emit(e.containerEvent(container, "create"))
	return container, nil
}

// Container returns a container by id, by the start of its id, or by
// name.
func (e *Engine) Container(reference string) (*Container, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.containerLocked(reference)
}

func (e *Engine) containerLocked(reference string) (*Container, error) {
	reference = strings.TrimPrefix(reference, "/")
	if container, has := e.containers[reference]; has {
		return container, nil
	}
	for _, container := range e.containers {
		if container.Spec.Name == reference {
			return container, nil
		}
	}
	for _, container := range e.containers {
		if reference != "" && strings.HasPrefix(container.ID, reference) {
			return container, nil
		}
	}
	return nil, fmt.Errorf("%w: no such container: %s", ErrNotFound, reference)
}

// Containers returns every container, newest first.
func (e *Engine) Containers() []*Container {
	e.mu.Lock()
	defer e.mu.Unlock()
	containers := make([]*Container, 0, len(e.containers))
	for _, container := range e.containers {
		containers = append(containers, container)
	}
	sort.Slice(containers, func(i, j int) bool { return containers[i].Created.After(containers[j].Created) })
	return containers
}

// containerEvent returns an event about a container, with the
// attributes Docker's carry: the name, the image and every label.
func (e *Engine) containerEvent(c *Container, action string) Event {
	attributes := map[string]string{"name": c.Spec.Name, "image": c.Spec.Image}
	for key, value := range c.Spec.Labels {
		attributes[key] = value
	}
	return Event{Type: "container", Action: action, ID: c.ID, Attributes: attributes}
}

// PageURL returns the address of the container's page, for a dashboard
// to load, or "" while the container has none.
func (e *Engine) PageURL(c *Container) string {
	record, err := state.Load(e.config.Root, c.ShortID())
	if err != nil {
		return ""
	}
	return record.URL
}

// socket returns the container's supervisor's control socket.
func (e *Engine) socket(c *Container) string {
	return filepath.Join(state.Dir(e.config.Root, c.ShortID()), state.SocketFile)
}

// Start boots the container's guest and runs its program. It returns
// once the program is running.
func (e *Engine) Start(reference string) error {
	container, err := e.Container(reference)
	if err != nil {
		return err
	}

	container.mu.Lock()
	if container.status == StatusRunning {
		container.mu.Unlock()
		return nil
	}
	// A container started again gets a new run to wait on.
	if container.status == StatusExited {
		container.started = make(chan struct{})
		container.done = make(chan struct{})
		container.Log.Reopen()
	}
	started, done := container.started, container.done
	container.mu.Unlock()

	planned, err := e.plan(container)
	if err != nil {
		return e.failStart(container, err)
	}

	// The program's input and output are pipes this engine holds the
	// other ends of.
	stdinRead, stdinWrite, err := os.Pipe()
	if err != nil {
		return e.failStart(container, err)
	}
	stdoutRead, stdoutWrite, err := os.Pipe()
	if err != nil {
		return e.failStart(container, err)
	}
	stderrRead, stderrWrite, err := os.Pipe()
	if err != nil {
		return e.failStart(container, err)
	}
	closeAll := func(files ...*os.File) {
		for _, file := range files {
			file.Close()
		}
	}

	record := state.Container{ID: container.ShortID(), Status: state.Creating, Created: time.Now().UTC()}
	process, dir, err := runner.Create(record, planned, runner.Launch{
		Root:   e.config.Root,
		ID:     container.ShortID(),
		Stdin:  stdinRead,
		Stdout: stdoutWrite,
		Stderr: stderrWrite,
		Self:   e.config.Self,
		Start:  func(process *exec.Cmd) error { return process.Start() },
	})
	closeAll(stdinRead, stdoutWrite, stderrWrite)
	if err != nil {
		closeAll(stdinWrite, stdoutRead, stderrRead)
		return e.failStart(container, err)
	}
	if err := supervisor.Start(filepath.Join(dir, state.SocketFile)); err != nil {
		process.Process.Kill()
		process.Wait()
		closeAll(stdinWrite, stdoutRead, stderrRead)
		state.Remove(e.config.Root, container.ShortID())
		return e.failStart(container, err)
	}

	// A program whose input nobody may write has none.
	if !container.Spec.OpenStdin {
		stdinWrite.Close()
		stdinWrite = nil
	}

	container.mu.Lock()
	container.status = StatusRunning
	container.pid = process.Process.Pid
	container.startedAt = time.Now()
	container.failure = ""
	container.stdin = stdinWrite
	container.mu.Unlock()
	close(started)
	e.events.emit(e.containerEvent(container, "start"))

	// What the program prints goes into the log, each stream as its own.
	var copies sync.WaitGroup
	for stream, source := range map[byte]*os.File{StreamStdout: stdoutRead, StreamStderr: stderrRead} {
		copies.Add(1)
		go func() {
			defer copies.Done()
			defer source.Close()
			buffer := make([]byte, inputChunkSize)
			for {
				count, err := source.Read(buffer)
				if count > 0 {
					container.Log.Write(stream, buffer[:count])
				}
				if err != nil {
					return
				}
			}
		}()
	}

	// The supervisor's exit is the container's, and its status the
	// program's.
	go func() {
		status := 0
		err := process.Wait()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			status = exit.ExitCode()
			if status < 0 {
				status = exitKilled
			}
		}
		copies.Wait()

		container.mu.Lock()
		container.status = StatusExited
		container.exitCode = status
		container.finishedAt = time.Now()
		container.pid = 0
		if container.stdin != nil {
			container.stdin.Close()
			container.stdin = nil
		}
		container.mu.Unlock()

		container.Log.Close()
		state.Remove(e.config.Root, container.ShortID())
		event := e.containerEvent(container, "die")
		event.Attributes["exitCode"] = fmt.Sprint(status)
		e.events.emit(event)
		close(done)

		if container.Spec.AutoRemove {
			e.Remove(container.ID, false)
		}
	}()
	return nil
}

// failStart records why a container did not start and ends the run
// anyone is waiting on.
func (e *Engine) failStart(c *Container, err error) error {
	c.mu.Lock()
	c.status = StatusExited
	c.exitCode = supervisor.ExitFailed
	c.failure = err.Error()
	c.finishedAt = time.Now()
	done := c.done
	c.mu.Unlock()

	c.Log.Close()
	select {
	case <-done:
	default:
		close(done)
	}
	return err
}

// plan turns a container's description into what the supervisor runs:
// the image's own configuration with the description's changes on top.
func (e *Engine) plan(c *Container) (plan.Plan, error) {
	spec := c.Spec
	pulled, err := e.Image(spec.Image)
	if err != nil {
		return plan.Plan{}, err
	}

	overrides := image.Overrides{
		Command:           spec.Cmd,
		Entrypoint:        spec.Entrypoint,
		ReplaceEntrypoint: spec.ReplaceEntrypoint,
		Env:               spec.Env,
		Cwd:               spec.WorkingDir,
		User:              spec.User,
	}
	if spec.Tty {
		overrides.Env = append([]string{envTerm}, overrides.Env...)
	}

	// The image's own files are where a user given by name is found.
	tree, err := rootfs.OpenLayers(pulled.Layers)
	if err != nil {
		return plan.Plan{}, err
	}
	guestSpec, err := image.Spec(pulled.Config, overrides, tree)
	tree.Close()
	if err != nil {
		return plan.Plan{}, err
	}
	guestSpec.Hostname = c.ShortID()
	guestSpec.Files["/etc/hostname"] = c.ShortID() + "\n"

	planned := plan.Plan{
		Spec:        guestSpec,
		Terminal:    plan.TerminalNone,
		MemoryBytes: spec.MemoryBytes,
		CPUs:        spec.CPUs,
		Layers:      pulled.Layers,
		Publish:     spec.Publish,
		Browser:     e.config.Browser,
		Title:       spec.Name,
	}
	if spec.Tty {
		planned.Terminal = plan.TerminalInherit
	}

	// On each network the container answers to its name, its short id
	// and whatever else its creator called it there.
	for network, aliases := range spec.Networks {
		if e.isBuiltin(network) {
			continue
		}
		names := append([]string{spec.Name, c.ShortID()}, aliases...)
		planned.Networks = append(planned.Networks, plan.Network{Name: network, Names: names})
	}

	if err := e.planMounts(&planned, spec.Mounts); err != nil {
		return plan.Plan{}, err
	}
	return planned, nil
}

// planMounts adds a container's mounts to its plan. A single small file
// is copied into the container by content, as an engine's bound
// configuration files are. A directory, or a volume, is carried live.
func (e *Engine) planMounts(planned *plan.Plan, mounts []Mount) error {
	for _, mount := range mounts {
		source := mount.Source

		// A source that is not a path names a volume, made on first use.
		if !filepath.IsAbs(source) {
			volume, err := e.CreateVolume(source, nil)
			if err != nil {
				return err
			}
			source = volume.Path
		}

		info, err := os.Stat(source)
		if err != nil {
			return fmt.Errorf("mount %s: %w", mount.Source, err)
		}
		if info.Mode().IsRegular() {
			if info.Size() > maxBoundFile {
				return fmt.Errorf("mount %s: a single file over %d bytes cannot be mounted", mount.Source, maxBoundFile)
			}
			content, err := os.ReadFile(source)
			if err != nil {
				return err
			}
			planned.Spec.Files[filepath.Clean(mount.Target)] = string(content)
			continue
		}
		planned.Volumes = append(planned.Volumes, plan.Volume{
			Source:   source,
			Target:   filepath.Clean(mount.Target),
			ReadOnly: mount.ReadOnly,
		})
	}
	return nil
}

// isBuiltin reports whether a network is one of Docker's three.
func (e *Engine) isBuiltin(name string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	network, has := e.networks[name]
	return has && network.Builtin
}

// Kill sends the container's program a signal.
func (e *Engine) Kill(reference, signal string) error {
	container, err := e.Container(reference)
	if err != nil {
		return err
	}
	if container.State().Status != StatusRunning {
		return fmt.Errorf("%w: container %s is not running", ErrConflict, container.ShortID())
	}
	name, err := signals.Parse(signal)
	if err != nil {
		return err
	}
	e.events.emit(e.containerEvent(container, "kill"))
	return supervisor.Kill(e.socket(container), name)
}

// Stop asks the container's program to end, and after timeout makes it.
func (e *Engine) Stop(reference string, timeout time.Duration) error {
	container, err := e.Container(reference)
	if err != nil {
		return err
	}
	state := container.State()
	if state.Status != StatusRunning {
		return nil
	}
	done := container.Done()

	supervisor.Kill(e.socket(container), signalStop)
	select {
	case <-done:
		return nil
	case <-time.After(timeout):
	}

	supervisor.Kill(e.socket(container), signalKill)
	select {
	case <-done:
		return nil
	case <-time.After(killWait):
	}

	// A guest that does not answer even that is ended from outside: the
	// supervisor leads a session whose process group is it and its
	// browser.
	syscall.Kill(-state.Pid, syscall.SIGKILL)
	<-done
	return nil
}

// Remove forgets a container. A running one is removed only with force,
// which stops it first.
func (e *Engine) Remove(reference string, force bool) error {
	container, err := e.Container(reference)
	if err != nil {
		return err
	}
	if container.State().Status == StatusRunning {
		if !force {
			return fmt.Errorf("%w: container %s is running: stop it first, or force the removal", ErrConflict, container.ShortID())
		}
		e.Stop(container.ID, 0)
	}

	e.mu.Lock()
	_, present := e.containers[container.ID]
	delete(e.containers, container.ID)
	e.mu.Unlock()
	if !present {
		return nil
	}

	state.Remove(e.config.Root, container.ShortID())
	e.events.emit(e.containerEvent(container, "destroy"))
	close(container.removed)
	return nil
}

// Resize tells the container's program its terminal's new size.
func (e *Engine) Resize(reference string, cols, rows int) error {
	container, err := e.Container(reference)
	if err != nil {
		return err
	}
	if container.State().Status != StatusRunning {
		return nil
	}
	return supervisor.Resize(e.socket(container), cols, rows)
}

// WriteInput copies input to the container's program until the input
// ends, and then, if the container was made to, ends the program's
// input too.
func (e *Engine) WriteInput(c *Container, input io.Reader) {
	// A client attaches before it starts the container, so its input
	// may be here before there is a program to give it to.
	c.mu.Lock()
	started, done := c.started, c.done
	c.mu.Unlock()
	select {
	case <-started:
	case <-done:
		return
	}

	buffer := make([]byte, inputChunkSize)
	for {
		count, err := input.Read(buffer)
		if count > 0 {
			c.mu.Lock()
			stdin := c.stdin
			c.mu.Unlock()
			if stdin == nil {
				return
			}
			if _, err := stdin.Write(buffer[:count]); err != nil {
				return
			}
		}
		if err != nil {
			break
		}
	}

	if !c.Spec.StdinOnce {
		return
	}
	c.mu.Lock()
	if c.stdin != nil {
		c.stdin.Close()
		c.stdin = nil
	}
	c.mu.Unlock()
}
