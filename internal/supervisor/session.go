package supervisor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"

	"github.com/coder/websocket"
)

// The page's side of the conversation is page/main.js; the two files
// name the same messages and streams.

// The first byte of a binary message says which stream its bytes are.
const (
	streamStdin  = 0
	streamStdout = 1
	streamStderr = 2
)

// The messages in each direction, by their `t` field.
const (
	toPageStart  = "start"
	toPageEOF    = "eof"
	toPageKill   = "kill"
	toPageResize = "resize"

	fromPageReady = "ready"
	fromPageExit  = "exit"
	fromPageLog   = "log"
	fromPageError = "error"
)

// maxMessage bounds one WebSocket message. The page sends output in
// chunks far below it; the bound only stops a page that was taken over
// from asking the supervisor for unbounded memory.
const maxMessage = 16 << 20

// pageMessage is a text message in either direction.
type pageMessage struct {
	Type    string `json:"t"`
	Message string `json:"message,omitempty"`
	Status  int    `json:"status,omitempty"`
	Signal  string `json:"signal,omitempty"`
	Cols    int    `json:"cols,omitempty"`
	Rows    int    `json:"rows,omitempty"`
}

// session is the supervisor's conversation with the container's page.
type session struct {
	// stdout and stderr are where the program's output goes.
	stdout io.Writer
	stderr io.Writer
	// logf records what the page reports about itself.
	logf func(format string, args ...any)

	// ready is closed when the guest is up, exit carries the program's
	// status, and failed what went wrong in the page.
	ready  chan struct{}
	exit   chan int
	failed chan string

	// socket is the page's connection, set once the page connects.
	mu     sync.Mutex
	socket *websocket.Conn
	gone   chan struct{}

	readyOnce sync.Once
}

func newSession(stdout, stderr io.Writer, logf func(string, ...any)) *session {
	return &session{
		stdout: stdout,
		stderr: stderr,
		logf:   logf,
		ready:  make(chan struct{}),
		exit:   make(chan int, 1),
		failed: make(chan string, 1),
		gone:   make(chan struct{}),
	}
}

// serve is the page's WebSocket: it reads what the page sends until the
// page goes away.
func (s *session) serve(w http.ResponseWriter, r *http.Request) {
	socket, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	socket.SetReadLimit(maxMessage)

	// One page per container: a second connection is not the page the
	// supervisor started.
	s.mu.Lock()
	if s.socket != nil {
		s.mu.Unlock()
		socket.Close(websocket.StatusPolicyViolation, "the page is already connected")
		return
	}
	s.socket = socket
	s.mu.Unlock()
	s.logf("page connected")
	defer close(s.gone)
	defer socket.CloseNow()

	ctx := r.Context()
	for {
		kind, data, err := socket.Read(ctx)
		if err != nil {
			return
		}

		// Output: the stream's number, then its bytes.
		if kind == websocket.MessageBinary {
			if len(data) == 0 {
				continue
			}
			switch data[0] {
			case streamStdout:
				s.stdout.Write(data[1:])
			case streamStderr:
				s.stderr.Write(data[1:])
			}
			continue
		}

		var message pageMessage
		if err := json.Unmarshal(data, &message); err != nil {
			s.logf("page: unreadable message: %v", err)
			continue
		}
		switch message.Type {
		case fromPageReady:
			s.readyOnce.Do(func() { close(s.ready) })
		case fromPageExit:
			select {
			case s.exit <- message.Status:
			default:
			}
		case fromPageLog:
			s.logf("guest: %s", message.Message)
		case fromPageError:
			select {
			case s.failed <- message.Message:
			default:
			}
		}
	}
}

// send writes one text message to the page.
func (s *session) send(message pageMessage) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	return s.write(websocket.MessageText, data)
}

// sendInput writes bytes for the program's input.
func (s *session) sendInput(data []byte) error {
	framed := make([]byte, 0, len(data)+1)
	framed = append(framed, streamStdin)
	framed = append(framed, data...)
	return s.write(websocket.MessageBinary, framed)
}

// write sends one message, one at a time: input, signals and sizes come
// from different goroutines.
func (s *session) write(kind websocket.MessageType, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.socket == nil {
		return io.ErrClosedPipe
	}
	return s.socket.Write(context.Background(), kind, data)
}
