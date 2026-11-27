package supervisor

import (
	"context"
	"encoding/json"
	"errors"
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
	// toPageConnect asks the page for a connection into the guest
	// (inbound.go).
	toPageConnect = "connect"
	// toPageWelcome tells a page it is the container's page, and
	// toPageTaken that another page already is: the same address opened
	// in a second tab.
	toPageWelcome = "welcome"
	toPageTaken   = "taken"
	// The messages that park a guest and resume one are in park.go.

	fromPageReady = "ready"
	fromPageExit  = "exit"
	fromPageLog   = "log"
	fromPageError = "error"
	// fromPageListen and fromPageUnlisten report a port the container's
	// program opened or closed.
	fromPageListen   = "listen"
	fromPageUnlisten = "unlisten"
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
	// Port is a port in the guest, and Connection the number of a
	// connection the page is asked to open to one.
	Port       int    `json:"port,omitempty"`
	Connection string `json:"id,omitempty"`
	// Spec, Terminal and Term describe a second program the page is
	// asked to start (exec.go): what init is told, whether the program
	// gets a terminal, and the terminal's type.
	Spec     string `json:"spec,omitempty"`
	Terminal bool   `json:"terminal,omitempty"`
	Term     string `json:"term,omitempty"`
	// Restore, in a welcome, tells the page to resume the parked guest
	// from its snapshot in place of booting one, and Stats are a page's
	// numbers for a park or a resume (park.go).
	Restore bool  `json:"restore,omitempty"`
	Stats   Stats `json:"stats,omitempty"`
}

// session is the supervisor's conversation with the container's page.
type session struct {
	// stdout and stderr are where the program's output goes.
	stdout io.Writer
	stderr io.Writer
	// logf records what the page reports about itself.
	logf func(format string, args ...any)
	// onListen is told when the container's program opens or closes a
	// port, nil when nobody mirrors ports.
	onListen func(port int, open bool)

	// ready is closed when the guest is up, exit carries the program's
	// status, and failed what went wrong in the page.
	ready  chan struct{}
	exit   chan int
	failed chan string

	// socket is the page's connection, set once the page connects, and
	// gone is closed when the page that is the container goes away.
	mu     sync.Mutex
	socket *websocket.Conn
	gone   chan struct{}

	// What a container's move to another page needs (park.go): where the
	// move stands, a condition signalled when that changes, the answer
	// the park in progress waits for, the signals kept for the page that
	// resumes, and what to do once a page has resumed.
	phase       phase
	changed     *sync.Cond
	parkAnswer  chan parkOutcome
	heldSignals []string
	onResumed   func()

	readyOnce sync.Once
}

func newSession(stdout, stderr io.Writer, logf func(string, ...any)) *session {
	s := &session{
		stdout: stdout,
		stderr: stderr,
		logf:   logf,
		ready:  make(chan struct{}),
		exit:   make(chan int, 1),
		failed: make(chan string, 1),
		gone:   make(chan struct{}),
	}
	s.changed = sync.NewCond(&s.mu)
	return s
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
	// supervisor started. The first page to connect after the guest was
	// parked is the container's next page, and is told to restore.
	s.mu.Lock()
	if s.socket != nil {
		s.mu.Unlock()
		taken, _ := json.Marshal(pageMessage{Type: toPageTaken})
		socket.Write(r.Context(), websocket.MessageText, taken)
		socket.Close(websocket.StatusNormalClosure, "the container already has a page")
		return
	}
	s.socket = socket
	restore := s.phase == phaseParked
	if restore {
		s.setPhase(phaseResuming)
	}
	s.mu.Unlock()
	s.logf("page connected")
	defer s.left(socket)
	defer socket.CloseNow()
	if err := s.send(pageMessage{Type: toPageWelcome, Restore: restore}); err != nil {
		return
	}

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
		case fromPageListen, fromPageUnlisten:
			if s.onListen != nil {
				s.onListen(message.Port, message.Type == fromPageListen)
			}
		case fromPageParked:
			// The page has stopped its guest and is let go.
			if s.parked(socket, message.Stats) {
				socket.Close(websocket.StatusNormalClosure, "parked")
				return
			}
		case fromPageParkFailed:
			s.endPark(parkOutcome{err: errors.New(message.Message)})
		case fromPageResumed:
			s.resumed(message.Stats)
		case fromPageError:
			// A page that could not restore the snapshot is not the
			// container's failure: the next page may restore it.
			if s.resumeFailed(socket, message.Message) {
				socket.Close(websocket.StatusNormalClosure, "the guest stays parked")
				return
			}
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

// sendInput writes bytes for the program's input. Input for a guest
// that is being moved to another page waits for the page that resumes
// it.
func (s *session) sendInput(data []byte) error {
	framed := make([]byte, 0, len(data)+1)
	framed = append(framed, streamStdin)
	framed = append(framed, data...)
	// A park may begin between the wait and the write, and the bytes
	// then wait for the next page like any that came later.
	for {
		s.waitLive()
		err := s.write(websocket.MessageBinary, framed)
		if !errors.Is(err, errParked) {
			return err
		}
	}
}

// write sends one message, one at a time: input, signals and sizes come
// from different goroutines.
func (s *session) write(kind websocket.MessageType, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.socket == nil {
		if s.phase == phaseParked {
			return errParked
		}
		return io.ErrClosedPipe
	}
	return s.socket.Write(context.Background(), kind, data)
}
