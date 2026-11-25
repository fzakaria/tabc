package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// The guest's network. The page's network stack ends the guest's
// Ethernet frames and hands each TCP connection and each name lookup to
// the supervisor, which makes them for the guest. The supervisor runs
// inside the container's network namespace (cmd/tabc, create), so a
// connection leaves from the address the engine gave the container and
// obeys whatever the engine set up around it: a container with no
// network reaches nothing.

// The parameters of a lookup and of a connection.
const (
	paramName = "name"
	paramHost = "host"
	paramPort = "port"
)

// The messages on a connection's WebSocket, by their `t` field. Bytes
// travel as binary messages.
const (
	netOpen  = "open"
	netEnd   = "end"
	netError = "error"
	netAck   = "ack"
)

// How long a dial and a lookup may take.
const (
	dialTimeout   = 30 * time.Second
	lookupTimeout = 10 * time.Second
)

// readChunk is how much is read from a peer at a time, and window how
// much may be on the way to the page and not yet taken by the guest
// before the supervisor stops reading from the peer. Without a window a
// fast peer and a slow guest would pile the difference up in the page.
const (
	readChunk = 64 << 10
	window    = 4 << 20
)

// The resolver configuration's directive for a server, the DNS port,
// and the networks dialled and looked up on.
const (
	directiveNameserver = "nameserver"
	dnsPort             = "53"
	networkTCP4         = "tcp4"
	networkIP4          = "ip4"
)

// netMessage is a text message on a connection's WebSocket.
type netMessage struct {
	Type    string `json:"t"`
	Message string `json:"message,omitempty"`
	Count   int    `json:"n,omitempty"`
}

// resolverFor returns a resolver that asks the servers a container's
// resolver configuration names. The supervisor's own configuration is
// the host's, and the host's servers may not exist on the container's
// network: an engine writes the container a list that does. With no
// usable configuration the default resolver is returned.
func resolverFor(resolvConf string) *net.Resolver {
	var servers []string
	scanner := bufio.NewScanner(strings.NewReader(resolvConf))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[0] == directiveNameserver && net.ParseIP(fields[1]) != nil {
			servers = append(servers, net.JoinHostPort(fields[1], dnsPort))
		}
	}
	if len(servers) == 0 {
		return net.DefaultResolver
	}

	// The Go resolver names the server to dial from the host's
	// configuration; the container's servers are dialled instead, each
	// in turn.
	var next int
	var mu sync.Mutex
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			mu.Lock()
			server := servers[next%len(servers)]
			next++
			mu.Unlock()

			var dialer net.Dialer
			return dialer.DialContext(ctx, network, server)
		},
	}
}

// serveLookup answers a name with its IPv4 addresses, which is all the
// guest's network stack carries.
func (s *server) serveLookup(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), lookupTimeout)
	defer cancel()

	addresses, err := s.resolver.LookupIP(ctx, networkIP4, r.URL.Query().Get(paramName))
	if err != nil || len(addresses) == 0 {
		http.Error(w, "no such host", http.StatusNotFound)
		return
	}

	names := make([]string, 0, len(addresses))
	for _, address := range addresses {
		names = append(names, address.String())
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(names)
}

// serveConnection carries one of the guest's TCP connections: the page
// opens a WebSocket naming the host and port, the supervisor dials, and
// bytes go both ways until both sides are done.
func (s *server) serveConnection(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	host := query.Get(paramHost)
	port, err := strconv.Atoi(query.Get(paramPort))
	if err != nil || host == "" {
		http.Error(w, "host and port are required", http.StatusBadRequest)
		return
	}

	socket, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	socket.SetReadLimit(maxMessage)
	defer socket.CloseNow()
	ctx := r.Context()

	send := func(message netMessage) error {
		data, _ := json.Marshal(message)
		return socket.Write(ctx, websocket.MessageText, data)
	}

	// The dial is the guest's connect: its failure is the guest's reset.
	dialer := net.Dialer{Timeout: dialTimeout}
	peer, err := dialer.DialContext(ctx, networkTCP4, net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		send(netMessage{Type: netError, Message: err.Error()})
		socket.Close(websocket.StatusNormalClosure, "")
		return
	}
	defer peer.Close()
	if err := send(netMessage{Type: netOpen}); err != nil {
		return
	}

	credit := newCredit(window)

	// Guest to peer, until the page closes the WebSocket: bytes, the
	// guest's half-close, and how much of the peer's data the guest has
	// taken.
	guestDone := make(chan struct{})
	go func() {
		defer close(guestDone)
		defer credit.close()
		for {
			kind, data, err := socket.Read(ctx)
			if err != nil {
				return
			}
			if kind == websocket.MessageBinary {
				if _, err := peer.Write(data); err != nil {
					return
				}
				continue
			}

			var message netMessage
			if err := json.Unmarshal(data, &message); err != nil {
				return
			}
			switch message.Type {
			case netEnd:
				if tcp, ok := peer.(*net.TCPConn); ok {
					tcp.CloseWrite()
				}
			case netAck:
				credit.give(message.Count)
			}
		}
	}()

	// Peer to guest, no faster than the guest takes it.
	buffer := make([]byte, readChunk)
	for {
		if !credit.wait() {
			return
		}
		count, err := peer.Read(buffer)
		if count > 0 {
			credit.take(count)
			if err := socket.Write(ctx, websocket.MessageBinary, buffer[:count]); err != nil {
				return
			}
		}
		if err == nil {
			continue
		}

		// The peer is done sending. The guest may still be, so the
		// connection stays until the page closes its side.
		if errors.Is(err, io.EOF) {
			send(netMessage{Type: netEnd})
			<-guestDone
			socket.Close(websocket.StatusNormalClosure, "")
			return
		}
		send(netMessage{Type: netError, Message: err.Error()})
		socket.Close(websocket.StatusNormalClosure, "")
		return
	}
}

// credit counts the bytes on their way to the guest against a window.
type credit struct {
	mu        sync.Mutex
	changed   *sync.Cond
	available int
	closed    bool
}

func newCredit(size int) *credit {
	c := &credit{available: size}
	c.changed = sync.NewCond(&c.mu)
	return c
}

// wait blocks until there is room in the window, and reports false once
// the connection is gone.
func (c *credit) wait() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for c.available <= 0 && !c.closed {
		c.changed.Wait()
	}
	return !c.closed
}

// take counts bytes sent towards the guest.
func (c *credit) take(count int) {
	c.mu.Lock()
	c.available -= count
	c.mu.Unlock()
}

// give counts bytes the guest has taken.
func (c *credit) give(count int) {
	c.mu.Lock()
	c.available += count
	c.mu.Unlock()
	c.changed.Broadcast()
}

// close releases a waiter for good.
func (c *credit) close() {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	c.changed.Broadcast()
}
