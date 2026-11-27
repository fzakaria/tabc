package supervisor

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// testWait is how long a test waits for something the supervisor's side
// does in another goroutine.
const testWait = 5 * time.Second

// parkedContainer is a session and the snapshot store behind a test
// server, the way the supervisor's own server routes them: the control
// WebSocket at /ctl and the snapshot at /snapshot.
type parkedContainer struct {
	conversation *session
	store        *parking
	server       *httptest.Server
}

// newParkedContainer starts a session with a snapshot store in a
// directory of the test's own.
func newParkedContainer(t *testing.T) *parkedContainer {
	t.Helper()
	conversation := newSession(io.Discard, io.Discard, t.Logf)
	store := newParking(conversation, filepath.Join(t.TempDir(), snapshotFile), 1<<20, t.Logf)
	store.movable = true
	store.running = func() bool { return true }

	mux := http.NewServeMux()
	mux.HandleFunc("/"+routeControl, conversation.serve)
	mux.HandleFunc("/"+routeSnapshot, store.serveSnapshot)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return &parkedContainer{conversation: conversation, store: store, server: server}
}

// openPage connects a page to the session and returns the page's socket
// and the supervisor's first word to the page.
func (c *parkedContainer) openPage(t *testing.T) (*websocket.Conn, pageMessage) {
	t.Helper()
	address := "ws" + strings.TrimPrefix(c.server.URL, "http") + "/" + routeControl
	socket, _, err := websocket.Dial(context.Background(), address, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { socket.CloseNow() })
	return socket, readPageMessage(t, socket)
}

// storeSnapshot stores a snapshot as a page does, under a content
// encoding when one is given, and returns the answer's status.
func (c *parkedContainer) storeSnapshot(t *testing.T, body []byte, encoding string) int {
	t.Helper()
	request, err := http.NewRequest(http.MethodPut, c.server.URL+"/"+routeSnapshot, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if encoding != "" {
		request.Header.Set(headerContentEncoding, encoding)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	return response.StatusCode
}

// readPageMessage reads one text message a page was sent.
func readPageMessage(t *testing.T, socket *websocket.Conn) pageMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()
	_, data, err := socket.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var message pageMessage
	if err := json.Unmarshal(data, &message); err != nil {
		t.Fatal(err)
	}
	return message
}

// say sends one text message as a page.
func say(t *testing.T, socket *websocket.Conn, message pageMessage) {
	t.Helper()
	data, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if err := socket.Write(context.Background(), websocket.MessageText, data); err != nil {
		t.Fatal(err)
	}
}

// waitPhase waits until the session is in a phase.
func waitPhase(t *testing.T, conversation *session, want phase) {
	t.Helper()
	deadline := time.Now().Add(testWait)
	for !conversation.inPhase(want) {
		if time.Now().After(deadline) {
			t.Fatalf("the session never reached phase %d", want)
		}
		time.Sleep(time.Millisecond)
	}
}

// isGone reports whether the session gave its page up for lost.
func isGone(conversation *session) bool {
	select {
	case <-conversation.gone:
		return true
	default:
		return false
	}
}

// parkOutcomeOf starts a park and returns a channel with its result.
func parkOutcomeOf(conversation *session) <-chan error {
	outcome := make(chan error, 1)
	go func() {
		_, err := conversation.park(testWait)
		outcome <- err
	}()
	return outcome
}

// TestParkAndResume checks a container's move from one page to the
// next: the first page is asked to park, stores a snapshot and says it
// parked; its leaving is not the container's end; the next page is told
// to restore, fetches the snapshot the first page stored and says it
// resumed; and only that page's leaving ends the container. Two
// WebSocket clients stand in for the two pages.
func TestParkAndResume(t *testing.T) {
	container := newParkedContainer(t)
	conversation := container.conversation
	snapshot := []byte("the guest's memory")

	// The first page is the container, booted and not restored.
	first, welcome := container.openPage(t)
	if welcome.Type != toPageWelcome || welcome.Restore {
		t.Fatalf("the first page was told %+v", welcome)
	}

	// Asked to park, the page stores the snapshot and then says so.
	parked := parkOutcomeOf(conversation)
	if asked := readPageMessage(t, first); asked.Type != toPagePark {
		t.Fatalf("the page was asked %+v, not to park", asked)
	}
	if status := container.storeSnapshot(t, snapshot, ""); status != http.StatusNoContent {
		t.Fatalf("storing the snapshot answered %d", status)
	}
	say(t, first, pageMessage{Type: fromPageParked})
	if err := <-parked; err != nil {
		t.Fatalf("park: %v", err)
	}

	// The page that parked goes, and the container does not.
	first.CloseNow()
	time.Sleep(50 * time.Millisecond)
	if isGone(conversation) {
		t.Fatal("the parked container ended with its first page")
	}

	// The next page is told to restore, and is served the snapshot.
	second, welcome := container.openPage(t)
	if welcome.Type != toPageWelcome || !welcome.Restore {
		t.Fatalf("the second page was told %+v", welcome)
	}
	served, err := http.Get(container.server.URL + "/" + routeSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(served.Body)
	served.Body.Close()
	if !bytes.Equal(body, snapshot) {
		t.Fatalf("the second page was served %q", body)
	}
	say(t, second, pageMessage{Type: fromPageResumed})
	waitPhase(t, conversation, phaseLive)

	// The page that resumed is the container now.
	second.CloseNow()
	select {
	case <-conversation.gone:
	case <-time.After(testWait):
		t.Fatal("the container outlived the page it resumed in")
	}
}

// TestPageLostWhileParking checks that a page which goes away after
// being asked to park, and before saying it parked, takes the container
// with it and fails the park: nothing says the snapshot is whole. A
// WebSocket client stands in for the page and closes instead of
// answering.
func TestPageLostWhileParking(t *testing.T) {
	container := newParkedContainer(t)
	page, _ := container.openPage(t)

	parked := parkOutcomeOf(container.conversation)
	readPageMessage(t, page)
	page.CloseNow()

	if err := <-parked; err == nil {
		t.Error("a park whose page went away succeeded")
	}
	select {
	case <-container.conversation.gone:
	case <-time.After(testWait):
		t.Error("the container outlived a page lost while parking")
	}
}

// TestParkRefused checks that a page which cannot save the guest leaves
// the container as it was: the park fails with the page's reason, the
// page is still the container, and a later park is asked of the page
// again. A WebSocket client stands in for the page.
func TestParkRefused(t *testing.T) {
	container := newParkedContainer(t)
	page, _ := container.openPage(t)

	parked := parkOutcomeOf(container.conversation)
	readPageMessage(t, page)
	say(t, page, pageMessage{Type: fromPageParkFailed, Message: "a task did not park"})
	err := <-parked
	if err == nil || !strings.Contains(err.Error(), "a task did not park") {
		t.Fatalf("park failed with %v", err)
	}
	if isGone(container.conversation) || !container.conversation.inPhase(phaseLive) {
		t.Fatal("a refused park did not leave the page as the container")
	}

	again := parkOutcomeOf(container.conversation)
	if asked := readPageMessage(t, page); asked.Type != toPagePark {
		t.Fatalf("the page was asked %+v, not to park", asked)
	}
	say(t, page, pageMessage{Type: fromPageParked})
	if err := <-again; err != nil {
		t.Fatalf("the second park: %v", err)
	}
}

// TestFailedResumeStaysParked checks that a page which cannot restore
// the snapshot does not end the container: its error is not the
// container's failure, and the next page is told to restore again. Two
// WebSocket clients stand in for the page that parks and the page that
// fails, and a third for the page after.
func TestFailedResumeStaysParked(t *testing.T) {
	container := newParkedContainer(t)
	conversation := container.conversation

	first, _ := container.openPage(t)
	parked := parkOutcomeOf(conversation)
	readPageMessage(t, first)
	say(t, first, pageMessage{Type: fromPageParked})
	if err := <-parked; err != nil {
		t.Fatal(err)
	}

	failing, welcome := container.openPage(t)
	if !welcome.Restore {
		t.Fatal("the page after a park was not told to restore")
	}
	say(t, failing, pageMessage{Type: fromPageError, Message: "not enough memory"})
	waitPhase(t, conversation, phaseParked)
	select {
	case message := <-conversation.failed:
		t.Fatalf("a failed resume failed the container: %s", message)
	default:
	}
	if isGone(conversation) {
		t.Fatal("a failed resume ended the container")
	}

	if _, welcome := container.openPage(t); !welcome.Restore {
		t.Error("the page after a failed resume was not told to restore")
	}
}

// TestSnapshotOnlyWhenWanted checks that the snapshot is taken from a
// page only while a park is in progress and given to one only while a
// resume is: a store or a fetch at any other time is refused. The
// requests are made against a session no page has connected to.
func TestSnapshotOnlyWhenWanted(t *testing.T) {
	container := newParkedContainer(t)
	address := container.server.URL + "/" + routeSnapshot

	if status := container.storeSnapshot(t, []byte("unasked"), ""); status != http.StatusConflict {
		t.Errorf("storing a snapshot nobody asked for answered %d", status)
	}

	fetched, err := http.Get(address)
	if err != nil {
		t.Fatal(err)
	}
	fetched.Body.Close()
	if fetched.StatusCode != http.StatusNotFound {
		t.Errorf("fetching a snapshot with none parked answered %d", fetched.StatusCode)
	}
}

// TestSignalsHeldWhileParked checks what a signal does to a container
// with no page: it is kept, and sent to the page that resumes, as a
// kill. One WebSocket client parks and another resumes and reads what
// the supervisor sends next.
func TestSignalsHeldWhileParked(t *testing.T) {
	container := newParkedContainer(t)
	conversation := container.conversation

	// A page that is the container takes its signals itself.
	first, _ := container.openPage(t)
	if conversation.holdSignal("TERM") {
		t.Fatal("a signal was held with a page to take it")
	}

	parked := parkOutcomeOf(conversation)
	readPageMessage(t, first)
	say(t, first, pageMessage{Type: fromPageParked})
	if err := <-parked; err != nil {
		t.Fatal(err)
	}
	if !conversation.holdSignal("TERM") {
		t.Fatal("a signal was not held with no page to take it")
	}

	second, _ := container.openPage(t)
	say(t, second, pageMessage{Type: fromPageResumed})
	if sent := readPageMessage(t, second); sent.Type != toPageKill || sent.Signal != "TERM" {
		t.Errorf("the page that resumed was sent %+v", sent)
	}
}

// TestStatsString checks how a page's numbers read in the log: each as
// name=value with no fraction, in the order of the names, whatever
// order the page sent them in.
func TestStatsString(t *testing.T) {
	stats := Stats{"storeMs": 98.4, "bytes": 33427436, "encodeMs": 17.6}
	want := "bytes=33427436 encodeMs=18 storeMs=98"
	if got := stats.String(); got != want {
		t.Errorf("stats read %q, want %q", got, want)
	}
}

// TestSnapshotEncodingKept checks that a snapshot a page stored
// compressed is served to the next page as compressed, so that the
// browser there undoes what the browser before did. One page stores
// gzip under its content encoding, and the fetch, whose client undoes
// gzip as a browser does, reads the bytes from before the compression.
func TestSnapshotEncodingKept(t *testing.T) {
	container := newParkedContainer(t)
	conversation := container.conversation
	plain := []byte("the guest's memory, which compresses well")
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	writer.Write(plain)
	writer.Close()

	first, _ := container.openPage(t)
	parked := parkOutcomeOf(conversation)
	readPageMessage(t, first)
	if status := container.storeSnapshot(t, compressed.Bytes(), encodingGzip); status != http.StatusNoContent {
		t.Fatalf("storing the snapshot answered %d", status)
	}
	say(t, first, pageMessage{Type: fromPageParked})
	if err := <-parked; err != nil {
		t.Fatal(err)
	}

	container.openPage(t)
	served, err := http.Get(container.server.URL + "/" + routeSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(served.Body)
	served.Body.Close()
	if !bytes.Equal(body, plain) {
		t.Errorf("the next page read %q", body)
	}
}
