package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Connections into the container. A program in the guest listens on a
// port, and nothing outside the tab can reach it: the guest's network
// ends in the page. So the supervisor listens on the host side, and
// for each connection it accepts asks the page to open one to the same
// port in the guest, and copies bytes between the two.
//
// The page cannot be connected to, only asked, so the last hop is the
// page's doing: the supervisor sends "connect" with a number over the
// control channel, and the page answers by opening a WebSocket that
// names the number.
//
// There are two reasons to listen. A port the container's program
// opens is mirrored at the container's own address, when the container
// has an address of its own: that is what a container engine's bridge,
// its port publishing and its other containers connect to. And a port
// published by `tabc run -p` is listened on at the host's address the
// flag names.

// paramConnection is the number a page's WebSocket answers.
const paramConnection = "id"

// The messages on an inbound connection's WebSocket. Bytes travel as
// binary messages; "end" says one side has no more to send.
const (
	inboundEnd   = "end"
	inboundError = "error"
)

// guestConnectTimeout is how long the page has to open a connection in
// the guest after being asked.
const guestConnectTimeout = 15 * time.Second

// inboundChunk is how much is read from a host connection at a time.
const inboundChunk = 32 << 10

// anyAddress is where a mirrored port listens: every address of the
// container's network namespace.
const anyAddress = ""

// inbound owns the supervisor's listeners and the connections the page
// has been asked for and has not yet opened.
type inbound struct {
	conversation *session
	logf         func(format string, args ...any)

	mu        sync.Mutex
	listeners map[string]net.Listener
	waiting   map[string]chan *guestConnection
	next      int
}

// guestConnection is a connection into the guest: the page's WebSocket,
// and a channel closed when the relay is done with the socket, which is
// when the request the socket arrived on may return.
type guestConnection struct {
	socket *websocket.Conn
	done   chan struct{}
}

func newInbound(conversation *session, logf func(string, ...any)) *inbound {
	return &inbound{
		conversation: conversation,
		logf:         logf,
		listeners:    map[string]net.Listener{},
		waiting:      map[string]chan *guestConnection{},
	}
}

// listen starts forwarding connections that arrive at hostIP:hostPort
// to guestPort in the guest. Listening twice on one address is not an
// error: the second is ignored.
func (in *inbound) listen(hostIP string, hostPort, guestPort int) error {
	address := net.JoinHostPort(hostIP, strconv.Itoa(hostPort))

	in.mu.Lock()
	defer in.mu.Unlock()
	if _, has := in.listeners[address]; has {
		return nil
	}

	listener, err := net.Listen(networkTCP4, address)
	if err != nil {
		return err
	}
	in.listeners[address] = listener
	go in.accept(listener, guestPort)
	return nil
}

// unlisten stops forwarding a mirrored port.
func (in *inbound) unlisten(port int) {
	address := net.JoinHostPort(anyAddress, strconv.Itoa(port))

	in.mu.Lock()
	defer in.mu.Unlock()
	if listener, has := in.listeners[address]; has {
		listener.Close()
		delete(in.listeners, address)
	}
}

// close stops every listener.
func (in *inbound) close() {
	in.mu.Lock()
	defer in.mu.Unlock()
	for address, listener := range in.listeners {
		listener.Close()
		delete(in.listeners, address)
	}
}

// accept forwards each connection a listener accepts, until the
// listener is closed.
func (in *inbound) accept(listener net.Listener, guestPort int) {
	for {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer connection.Close()
			if err := in.forward(connection, guestPort); err != nil {
				in.logf("inbound to port %d: %v", guestPort, err)
			}
		}()
	}
}

// forward carries one connection to a port in the guest.
func (in *inbound) forward(connection net.Conn, guestPort int) error {
	guest, err := in.dialGuest(guestPort)
	if err != nil {
		return err
	}
	defer close(guest.done)
	defer guest.socket.CloseNow()
	return relay(connection, guest.socket)
}

// dialGuest asks the page for a connection to a port in the guest and
// waits for the page's WebSocket.
func (in *inbound) dialGuest(port int) (*guestConnection, error) {
	in.mu.Lock()
	in.next++
	id := strconv.Itoa(in.next)
	arrived := make(chan *guestConnection, 1)
	in.waiting[id] = arrived
	in.mu.Unlock()

	defer func() {
		in.mu.Lock()
		delete(in.waiting, id)
		in.mu.Unlock()
	}()

	request := pageMessage{Type: toPageConnect, Connection: id, Port: port}
	if err := in.conversation.send(request); err != nil {
		return nil, err
	}
	select {
	case guest := <-arrived:
		return guest, nil
	case <-time.After(guestConnectTimeout):
		return nil, fmt.Errorf("the page did not connect within %s", guestConnectTimeout)
	}
}

// serve is the page's answer to a connect request: a WebSocket naming
// the request's number. The handler stays until the relay is done with
// the socket, since the socket lives only as long as its request.
func (in *inbound) serve(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get(paramConnection)

	in.mu.Lock()
	arrived, wanted := in.waiting[id]
	delete(in.waiting, id)
	in.mu.Unlock()
	if !wanted {
		http.Error(w, "no such connection", http.StatusNotFound)
		return
	}

	socket, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	socket.SetReadLimit(maxMessage)

	guest := &guestConnection{socket: socket, done: make(chan struct{})}
	arrived <- guest
	<-guest.done
}

// inboundMessage is a text message on an inbound connection.
type inboundMessage struct {
	Type    string `json:"t"`
	Message string `json:"message,omitempty"`
}

// relay copies bytes between a host connection and a guest connection
// until both directions are done. One side finishing its sending is
// passed on as that, not as the end of the connection: a client may
// send a request, say it is done, and still be waiting for the answer.
func relay(host net.Conn, guest *websocket.Conn) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	failed := make(chan error, 2)

	// Host to guest.
	go func() {
		buffer := make([]byte, inboundChunk)
		for {
			count, err := host.Read(buffer)
			if count > 0 {
				if err := guest.Write(ctx, websocket.MessageBinary, buffer[:count]); err != nil {
					failed <- err
					return
				}
			}
			if err == nil {
				continue
			}
			if !errors.Is(err, io.EOF) {
				failed <- err
				return
			}
			end, _ := json.Marshal(inboundMessage{Type: inboundEnd})
			failed <- guest.Write(ctx, websocket.MessageText, end)
			return
		}
	}()

	// Guest to host.
	go func() {
		for {
			kind, data, err := guest.Read(ctx)
			if err != nil {
				failed <- nil
				return
			}
			if kind == websocket.MessageBinary {
				if _, err := host.Write(data); err != nil {
					failed <- err
					return
				}
				continue
			}

			var message inboundMessage
			if err := json.Unmarshal(data, &message); err != nil {
				failed <- err
				return
			}
			switch message.Type {
			case inboundEnd:
				if tcp, ok := host.(*net.TCPConn); ok {
					tcp.CloseWrite()
				}
				failed <- nil
				return
			case inboundError:
				failed <- errors.New(message.Message)
				return
			}
		}
	}()

	// Both directions report once; the first error ends the relay.
	for range 2 {
		if err := <-failed; err != nil {
			return err
		}
	}
	return nil
}
