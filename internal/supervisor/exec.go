package supervisor

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/fzakaria/tabc/internal/guest"
)

// A second program in a running container. `tabc exec` is a process of
// its own, and the container's guest is reachable only through the
// container's page, so the request travels the way a connection into
// the guest does: the command asks the supervisor over the control
// socket, the supervisor asks the page, and the page starts the program
// in the guest and answers on a WebSocket that names the request.
//
// The control socket's connection stays open for the program's life and
// carries frames: the program's input one way, and its output and exit
// status the other.

// A frame is a kind, the length of its bytes as four bytes with the
// highest first, and the bytes. The first three kinds are the streams'
// numbers on the page's WebSocket (session.go). A control frame holds
// one JSON message: the end of input, a size or a signal from the
// command, and the exit status or an error from the supervisor.
const (
	frameStdin   = streamStdin
	frameStdout  = streamStdout
	frameStderr  = streamStderr
	frameControl = 3
)

// frameHeader is the length of a frame's kind and size, and maxFrame
// the most bytes one frame may hold.
const (
	frameHeader = 5
	maxFrame    = 1 << 20
)

// The messages only an exec's WebSocket and control frames carry. The
// rest are the session's: toPageEOF, toPageKill, toPageResize,
// fromPageExit and fromPageError.
const toPageExec = "exec"

// execStartTimeout is how long the page has to start the program and
// open its WebSocket.
const execStartTimeout = 30 * time.Second

// lostStatus is the status of a program whose container went away
// before the program's own status arrived.
const lostStatus = ExitFailed

// signalKill is the signal that ends a program whose command went away.
const signalKill = "KILL"

// Environment says what a second program's environment starts from.
type Environment int

const (
	// EnvironmentInherit is the container's environment with the
	// request's entries added.
	EnvironmentInherit Environment = iota
	// EnvironmentReplace is the request's entries and nothing else.
	EnvironmentReplace
)

// ExecUser is who a second program runs as.
type ExecUser struct {
	UID            uint32   `json:"uid"`
	GID            uint32   `json:"gid"`
	AdditionalGids []uint32 `json:"additionalGids,omitempty"`
}

// ExecRequest is a second program to run in a container.
type ExecRequest struct {
	// Args is the program and its arguments.
	Args []string `json:"args"`
	// Env is the program's environment, or what is added to the
	// container's, as Environment says.
	Env         []string    `json:"env,omitempty"`
	Environment Environment `json:"environment,omitempty"`
	// Cwd is the program's directory, empty for the container's.
	Cwd string `json:"cwd,omitempty"`
	// User is who the program runs as, in numbers, as an engine gives
	// a user. UserName is a user as a person writes one, "name",
	// "uid", "name:group" or "uid:gid", which the guest looks up in the
	// container's own files. With neither, the program runs as the
	// container's user; with both, as UserName.
	User     *ExecUser `json:"user,omitempty"`
	UserName string    `json:"userName,omitempty"`
	// Terminal gives the program a terminal of Cols by Rows cells.
	Terminal bool `json:"terminal,omitempty"`
	Cols     int  `json:"cols,omitempty"`
	Rows     int  `json:"rows,omitempty"`
}

// writeFrame writes one frame.
func writeFrame(w io.Writer, kind byte, payload []byte) error {
	framed := make([]byte, frameHeader, frameHeader+len(payload))
	framed[0] = kind
	binary.BigEndian.PutUint32(framed[1:], uint32(len(payload)))
	_, err := w.Write(append(framed, payload...))
	return err
}

// readFrame reads one frame.
func readFrame(r io.Reader) (byte, []byte, error) {
	var header [frameHeader]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, nil, err
	}
	size := binary.BigEndian.Uint32(header[1:])
	if size > maxFrame {
		return 0, nil, fmt.Errorf("a frame of %d bytes is larger than any frame is", size)
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return header[0], payload, nil
}

// execSpec returns what the guest's init is told about a second
// program: the request's own program, and the container's environment,
// directory and user wherever the request names none.
func execSpec(container guest.Spec, request ExecRequest) (guest.Spec, error) {
	if len(request.Args) == 0 {
		return guest.Spec{}, errors.New("exec needs a program")
	}

	spec := guest.Spec{
		Args:           request.Args,
		Cwd:            container.Cwd,
		UID:            container.UID,
		GID:            container.GID,
		AdditionalGids: container.AdditionalGids,
	}
	switch request.Environment {
	case EnvironmentReplace:
		spec.Env = request.Env
	default:
		spec.Env = append(append([]string{}, container.Env...), request.Env...)
	}
	if request.Cwd != "" {
		spec.Cwd = request.Cwd
	}
	if request.User != nil {
		spec.UID = request.User.UID
		spec.GID = request.User.GID
		spec.AdditionalGids = request.User.AdditionalGids
	}
	if request.UserName != "" {
		// The numbers are the guest's to find, and the container's
		// user's are not the named user's.
		spec.User = request.UserName
		spec.UID, spec.GID, spec.AdditionalGids = 0, 0, nil
	}
	return spec, nil
}

// ExecSession is the command's end of a second program: where the
// program's input goes and its output comes from.
type ExecSession struct {
	connection net.Conn
	reader     *bufio.Reader

	// Input, sizes and signals come from different goroutines.
	mu sync.Mutex
}

// StartExec asks the supervisor listening at path to run a second
// program, and returns once the program is running in the guest.
func StartExec(path string, request ExecRequest) (*ExecSession, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	connection, err := dialControl(path)
	if err != nil {
		return nil, err
	}

	// The same request and answer as any other, with a deadline. After
	// the answer the connection is the program's, for as long as the
	// program runs.
	connection.SetDeadline(time.Now().Add(execStartTimeout + controlTimeout))
	word := base64.StdEncoding.EncodeToString(encoded)
	if _, err := fmt.Fprintln(connection, requestExec, word); err != nil {
		connection.Close()
		return nil, err
	}

	// The reader is kept: the program's first output may arrive in the
	// same read as the answer.
	reader := bufio.NewReader(connection)
	if err := readAnswer(reader); err != nil {
		connection.Close()
		return nil, err
	}
	connection.SetDeadline(time.Time{})
	return &ExecSession{connection: connection, reader: reader}, nil
}

// Write sends bytes to the program's input.
func (s *ExecSession) Write(data []byte) (int, error) {
	// A frame holds only so much, and a caller may hand over more.
	for sent := 0; sent < len(data); {
		end := min(sent+maxFrame, len(data))
		if err := s.frame(frameStdin, data[sent:end]); err != nil {
			return sent, err
		}
		sent = end
	}
	return len(data), nil
}

// CloseInput tells the program its input has ended.
func (s *ExecSession) CloseInput() error {
	return s.control(pageMessage{Type: toPageEOF})
}

// Resize tells the program its terminal's new size.
func (s *ExecSession) Resize(cols, rows int) error {
	return s.control(pageMessage{Type: toPageResize, Cols: cols, Rows: rows})
}

// Kill sends the program a signal, by the name signals.Parse returns.
func (s *ExecSession) Kill(signal string) error {
	return s.control(pageMessage{Type: toPageKill, Signal: signal})
}

// Wait copies the program's output to stdout and stderr until the
// program ends, and returns its exit status.
func (s *ExecSession) Wait(stdout, stderr io.Writer) (int, error) {
	for {
		kind, payload, err := readFrame(s.reader)
		if err != nil {
			return lostStatus, errors.New("the container went away")
		}

		switch kind {
		case frameStdout:
			stdout.Write(payload)
		case frameStderr:
			stderr.Write(payload)
		case frameControl:
			var message pageMessage
			if err := json.Unmarshal(payload, &message); err != nil {
				continue
			}
			switch message.Type {
			case fromPageExit:
				return message.Status, nil
			case fromPageError:
				return lostStatus, errors.New(message.Message)
			}
		}
	}
}

// Close ends the session. A program still running is killed by the
// supervisor, which sees the connection go.
func (s *ExecSession) Close() error {
	return s.connection.Close()
}

// control sends one control message.
func (s *ExecSession) control(message pageMessage) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	return s.frame(frameControl, data)
}

// frame sends one frame, one at a time.
func (s *ExecSession) frame(kind byte, payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeFrame(s.connection, kind, payload)
}

// execs is the supervisor's side: the second programs the page has been
// asked to start and has not yet answered for.
type execs struct {
	conversation *session
	logf         func(format string, args ...any)

	// container is the container's own spec, where a second program's
	// environment, directory and user come from, and term the terminal
	// a second program is told it has.
	container guest.Spec
	term      string

	// running is set once the container's program has been started.
	// Before that the guest has no root to run a second program in.
	running atomic.Bool

	mu      sync.Mutex
	waiting map[string]chan *guestConnection
	next    int
}

func newExecs(conversation *session, container guest.Spec, term string, logf func(string, ...any)) *execs {
	return &execs{
		conversation: conversation,
		logf:         logf,
		container:    container,
		term:         term,
		waiting:      map[string]chan *guestConnection{},
	}
}

// handle serves one exec request on a control connection, from the
// request's line to the program's end. reader is what the request's
// line was read with.
func (e *execs) handle(connection net.Conn, reader *bufio.Reader, words []string) {
	program, request, err := e.start(words)
	if err != nil {
		fmt.Fprintln(connection, err.Error())
		return
	}
	defer close(program.done)
	defer program.socket.CloseNow()

	if _, err := fmt.Fprintln(connection, answerOK); err != nil {
		return
	}
	connection.SetDeadline(time.Time{})
	e.logf("exec %q", request.Args)

	// The command's input, sizes and signals, to the page. A command
	// that goes away takes its program with it, as a process that ends
	// takes the programs on its terminal.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		defer cancel()
		for {
			kind, payload, err := readFrame(reader)
			if err != nil {
				killed, _ := json.Marshal(pageMessage{Type: toPageKill, Signal: signalKill})
				program.socket.Write(ctx, websocket.MessageText, killed)
				return
			}

			switch kind {
			case frameStdin:
				framed := append([]byte{streamStdin}, payload...)
				err = program.socket.Write(ctx, websocket.MessageBinary, framed)
			case frameControl:
				err = program.socket.Write(ctx, websocket.MessageText, payload)
			}
			if err != nil {
				return
			}
		}
	}()

	// The program's output and its end, to the command.
	for {
		kind, data, err := program.socket.Read(ctx)
		if err != nil {
			lost, _ := json.Marshal(pageMessage{Type: fromPageError, Message: "the program's page went away"})
			writeFrame(connection, frameControl, lost)
			return
		}

		if kind == websocket.MessageBinary {
			if len(data) == 0 {
				continue
			}
			if err := writeFrame(connection, data[0], data[1:]); err != nil {
				return
			}
			continue
		}

		// Text is the end: the status, or why there is none.
		writeFrame(connection, frameControl, data)
		return
	}
}

// start reads an exec request, asks the page to start the program and
// waits for the page's WebSocket.
func (e *execs) start(words []string) (*guestConnection, ExecRequest, error) {
	var request ExecRequest
	if len(words) != 2 {
		return nil, request, errors.New("exec needs a request")
	}
	encoded, err := base64.StdEncoding.DecodeString(words[1])
	if err != nil {
		return nil, request, fmt.Errorf("the exec request: %w", err)
	}
	if err := json.Unmarshal(encoded, &request); err != nil {
		return nil, request, fmt.Errorf("the exec request: %w", err)
	}
	if !e.running.Load() {
		return nil, request, errors.New("the container is not running")
	}

	spec, err := execSpec(e.container, request)
	if err != nil {
		return nil, request, err
	}
	forInit, err := json.Marshal(spec)
	if err != nil {
		return nil, request, err
	}

	e.mu.Lock()
	e.next++
	id := strconv.Itoa(e.next)
	arrived := make(chan *guestConnection, 1)
	e.waiting[id] = arrived
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.waiting, id)
		e.mu.Unlock()
	}()

	// The spec goes to init as one argument, so the page hands it on as
	// it came.
	asked := pageMessage{
		Type:       toPageExec,
		Connection: id,
		Spec:       base64.StdEncoding.EncodeToString(forInit),
		Terminal:   request.Terminal,
		Cols:       orDefault(request.Cols, defaultCols),
		Rows:       orDefault(request.Rows, defaultRows),
		Term:       e.term,
	}
	if err := e.conversation.send(asked); err != nil {
		return nil, request, err
	}
	select {
	case program := <-arrived:
		return program, request, nil
	case <-time.After(execStartTimeout):
		return nil, request, fmt.Errorf("the page did not start the program within %s", execStartTimeout)
	}
}

// serve is the page's answer to an exec request: a WebSocket naming the
// request's number. The handler stays until the program is done with
// the socket, since the socket lives only as long as its request.
func (e *execs) serve(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get(paramConnection)

	e.mu.Lock()
	arrived, wanted := e.waiting[id]
	delete(e.waiting, id)
	e.mu.Unlock()
	if !wanted {
		http.Error(w, "no such exec", http.StatusNotFound)
		return
	}

	socket, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	socket.SetReadLimit(maxMessage)

	program := &guestConnection{socket: socket, done: make(chan struct{})}
	arrived <- program
	<-program.done
}
