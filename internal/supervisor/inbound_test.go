package supervisor

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

// TestRelay checks a connection carried into a guest and back: bytes
// from the outside reach the guest, the outside finishing its sending
// reaches the guest as that and not as a closed connection, and what
// the guest answers afterwards still reaches the outside, followed by
// the guest's own end. A WebSocket server stands in for the page: it
// collects what arrives until the outside is done, then answers with it
// in capitals.
func TestRelay(t *testing.T) {
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		socket, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer socket.CloseNow()
		ctx := r.Context()

		var request []byte
		for {
			kind, data, err := socket.Read(ctx)
			if err != nil {
				return
			}
			if kind == websocket.MessageText {
				break
			}
			request = append(request, data...)
		}

		socket.Write(ctx, websocket.MessageBinary, []byte(strings.ToUpper(string(request))))
		end, _ := json.Marshal(inboundMessage{Type: inboundEnd})
		socket.Write(ctx, websocket.MessageText, end)
		socket.Read(ctx)
	}))
	defer page.Close()

	guest, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(page.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer guest.CloseNow()

	// The outside: one end of a real TCP connection, the relay holding
	// the other.
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	relayed := make(chan error, 1)
	go func() {
		host, err := listener.Accept()
		if err != nil {
			relayed <- err
			return
		}
		defer host.Close()
		relayed <- relay(host, guest)
	}()

	outside, err := net.Dial("tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer outside.Close()
	if _, err := outside.Write([]byte("hello, guest")); err != nil {
		t.Fatal(err)
	}
	outside.(*net.TCPConn).CloseWrite()

	answer, err := io.ReadAll(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(answer) != "HELLO, GUEST" {
		t.Errorf("the outside read %q", answer)
	}
	if err := <-relayed; err != nil {
		t.Errorf("the relay ended with %v", err)
	}
}
