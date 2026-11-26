package engine

import (
	"fmt"
	"sync"

	"github.com/fzakaria/tabc/internal/supervisor"
)

// A second program in a running container, as the Docker command line
// asks for one: `exec` is created first, which only records what to
// run, and started after, which is when the program runs and its
// input and output are carried.

// ExecSpec is a second program as its creator described it.
type ExecSpec struct {
	Cmd        []string
	Env        []string
	WorkingDir string
	// User is a name or a number, with or without a group, as a
	// container's is. Empty is the container's own user.
	User string

	// Tty gives the program a terminal of Cols by Rows cells, and
	// AttachStdin an input.
	Tty         bool
	Cols        int
	Rows        int
	AttachStdin bool
}

// Exec is one second program and what has become of it.
type Exec struct {
	ID        string
	Container *Container
	Spec      ExecSpec

	mu       sync.Mutex
	session  *supervisor.ExecSession
	running  bool
	exitCode int
}

// ExecState is a second program's condition at one moment.
type ExecState struct {
	Running  bool
	ExitCode int
}

// State returns the program's condition now.
func (x *Exec) State() ExecState {
	x.mu.Lock()
	defer x.mu.Unlock()
	return ExecState{Running: x.running, ExitCode: x.exitCode}
}

// Finish records that the program ended with a status.
func (x *Exec) Finish(status int) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.running = false
	x.exitCode = status
}

// Resize tells the program its terminal's new size. Before the program
// runs the size is kept for when it does.
func (x *Exec) Resize(cols, rows int) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.session == nil {
		x.Spec.Cols, x.Spec.Rows = cols, rows
		return nil
	}
	return x.session.Resize(cols, rows)
}

// execRequest returns what the supervisor is asked for a second
// program. The user goes as the description wrote one, for the guest to
// look up in the running container's files.
func execRequest(spec ExecSpec) supervisor.ExecRequest {
	request := supervisor.ExecRequest{
		Args:     spec.Cmd,
		Env:      spec.Env,
		Cwd:      spec.WorkingDir,
		UserName: spec.User,
		Terminal: spec.Tty,
		Cols:     spec.Cols,
		Rows:     spec.Rows,
	}
	if spec.Tty {
		request.Env = append([]string{envTerm}, request.Env...)
	}
	return request
}

// CreateExec records a second program for a running container.
func (e *Engine) CreateExec(reference string, spec ExecSpec) (*Exec, error) {
	container, err := e.Container(reference)
	if err != nil {
		return nil, err
	}
	if container.State().Status != StatusRunning {
		return nil, fmt.Errorf("%w: container %s is not running", ErrConflict, container.ShortID())
	}
	if len(spec.Cmd) == 0 {
		return nil, fmt.Errorf("%w: exec needs a program", ErrConflict)
	}

	created := &Exec{ID: newID(), Container: container, Spec: spec}
	e.mu.Lock()
	e.execs[created.ID] = created
	e.mu.Unlock()
	return created, nil
}

// Exec returns a second program by its id.
func (e *Engine) Exec(id string) (*Exec, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	found, has := e.execs[id]
	if !has {
		return nil, fmt.Errorf("%w: no such exec: %s", ErrNotFound, id)
	}
	return found, nil
}

// StartExec runs a second program and returns the session its input
// and output travel on. Whoever takes the session calls Finish with
// the program's status.
func (e *Engine) StartExec(x *Exec) (*supervisor.ExecSession, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.session != nil {
		return nil, fmt.Errorf("%w: exec %s has already run", ErrConflict, x.ID[:shortID])
	}

	session, err := supervisor.StartExec(e.socket(x.Container), execRequest(x.Spec))
	if err != nil {
		return nil, err
	}
	x.session = session
	x.running = true
	return session, nil
}
