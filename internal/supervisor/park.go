package supervisor

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// Moving a container to another tab. The container's guest is a machine
// in one page's memory, and a page cannot be handed from one browser to
// another. What can be handed over is the machine: `tabc park` has the
// page save the guest (the unkernel SDK's snapshot) and give the bytes
// to the supervisor, and the guest stops there. The container is then
// parked: a snapshot in its state directory and no page. The next page
// to open the container's address is told to restore the snapshot in
// place of booting, and the program carries on from where the snapshot
// was taken, in the same process and with what it had in memory.
//
// The supervisor is the same one throughout, so what the supervisor
// holds stays: the program's output and input, published ports, names on
// tabc networks. What only the old page held is gone: the guest's open
// TCP connections and the second programs `tabc exec` started.

// The messages that park and resume a guest, by their `t` field.
const (
	// toPagePark asks the page to save the guest and stop it.
	toPagePark = "park"
	// fromPageParked says the snapshot is stored and the guest stopped,
	// and fromPageParkFailed that the guest could not be saved and runs
	// on.
	fromPageParked     = "parked"
	fromPageParkFailed = "parkfailed"
	// fromPageResumed says the page restored the snapshot and the
	// program runs again.
	fromPageResumed = "resumed"
)

// snapshotFile is the parked guest in the container's state directory,
// and snapshotMode its mode: a snapshot is the program's memory, with
// whatever secrets the program holds.
const (
	snapshotFile = "snapshot.bin"
	snapshotMode = 0o600
)

// temporarySuffix names the file a snapshot is written to before it
// takes the snapshot's own name.
const temporarySuffix = ".tmp"

// snapshotSlackBytes is how much larger than the guest's memory a
// snapshot may be: the SDK's header and a few bytes for each chunk of
// memory.
const snapshotSlackBytes = 16 << 20

// parkTimeout is how long a page has to save the guest and store the
// snapshot. The snapshot is tens of megabytes, and the page may be at
// the far end of a slow connection.
const parkTimeout = 2 * time.Minute

// requestPark is the control request `tabc park` sends. Its connection
// stays open until the page has answered, which is longer than other
// requests are given.
const requestPark = "park"

// phase is where the supervisor's conversation with a page stands.
type phase int

const (
	// phaseLive: the page that is connected, or the first page still to
	// connect, is the container.
	phaseLive phase = iota
	// phaseParking: the page was asked to save the guest and has not
	// answered.
	phaseParking
	// phaseParked: the guest is a snapshot the supervisor holds, and no
	// page is the container.
	phaseParked
	// phaseResuming: a page was told to restore the snapshot and has not
	// answered.
	phaseResuming
)

// Stats are the times and sizes a page reports for a park or a resume,
// by name: milliseconds and bytes.
type Stats map[string]float64

// String returns the numbers as the log shows them: name=value, in the
// order of the names.
func (stats Stats) String() string {
	names := make([]string, 0, len(stats))
	for name := range stats {
		names = append(names, name)
	}
	sort.Strings(names)

	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s=%.0f", name, stats[name]))
	}
	return strings.Join(parts, " ")
}

// parkOutcome is a page's answer to being asked to park.
type parkOutcome struct {
	stats Stats
	err   error
}

// The reasons a park is refused before the page is asked.
var (
	errParked        = errors.New("the container is parked: it has no page, and resumes in the next browser to open its address")
	errNoPageYet     = errors.New("the container has no page yet")
	errParkBusy      = errors.New("the container is already being parked or resumed")
	errParkTimeout   = errors.New("the page did not park the guest in time")
	errPageLeft      = errors.New("the page went away while parking, and the container with it")
	errNotMovable    = errors.New("the container cannot be parked: it was not started with --movable")
	errNotRunning    = errors.New("the container is not running")
	errSnapshotState = errors.New("no snapshot is wanted now")
)

// setPhase moves the conversation to a phase and wakes whoever waits
// for one. The caller holds s.mu.
func (s *session) setPhase(next phase) {
	s.phase = next
	s.changed.Broadcast()
}

// inPhase reports whether the conversation is in a phase.
func (s *session) inPhase(want phase) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.phase == want
}

// park asks the page to save the guest and waits for the page's answer:
// the page's numbers once the snapshot is stored and the guest stopped,
// or why the guest was not saved.
func (s *session) park(timeout time.Duration) (Stats, error) {
	// One park at a time, and only of a guest some page is running.
	s.mu.Lock()
	refusal := s.parkRefusal()
	if refusal != nil {
		s.mu.Unlock()
		return nil, refusal
	}
	answer := make(chan parkOutcome, 1)
	s.parkAnswer = answer
	s.setPhase(phaseParking)
	s.mu.Unlock()

	if err := s.send(pageMessage{Type: toPagePark}); err != nil {
		s.endPark(parkOutcome{err: err})
	}

	select {
	case outcome := <-answer:
		return outcome.stats, outcome.err
	case <-time.After(timeout):
		// The page may still answer. A late "parked" is honoured, since
		// the guest has stopped by then; the caller is only told that
		// nothing is known yet.
		return nil, errParkTimeout
	}
}

// parkRefusal returns why the guest cannot be parked now, or nil when a
// page is running it. The caller holds s.mu.
func (s *session) parkRefusal() error {
	switch {
	case s.phase == phaseParked:
		return errParked
	case s.phase != phaseLive:
		return errParkBusy
	case s.socket == nil:
		return errNoPageYet
	}
	return nil
}

// endPark answers the park in progress, if there is one, and makes the
// page the container again when the park failed.
func (s *session) endPark(outcome parkOutcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phase == phaseParking && outcome.err != nil {
		s.setPhase(phaseLive)
	}
	if s.parkAnswer == nil {
		return
	}
	s.parkAnswer <- outcome
	s.parkAnswer = nil
}

// parked records that the page on socket stored the snapshot and stopped
// the guest. The page is let go: the next page to connect is told to
// restore. It reports whether the page was parking at all.
func (s *session) parked(socket *websocket.Conn, stats Stats) bool {
	s.mu.Lock()
	if s.phase != phaseParking || s.socket != socket {
		s.mu.Unlock()
		return false
	}
	s.socket = nil
	s.setPhase(phaseParked)
	s.mu.Unlock()

	s.logf("parked: %s", stats)
	s.endPark(parkOutcome{stats: stats})
	return true
}

// resumed records that the page restored the snapshot: the page is the
// container, and takes the signals that were held for one.
func (s *session) resumed(stats Stats) {
	s.mu.Lock()
	if s.phase != phaseResuming {
		s.mu.Unlock()
		return
	}
	held := s.heldSignals
	s.heldSignals = nil
	s.setPhase(phaseLive)
	s.mu.Unlock()

	s.logf("resumed: %s", stats)
	if s.onResumed != nil {
		s.onResumed()
	}
	for _, name := range held {
		if err := s.send(pageMessage{Type: toPageKill, Signal: name}); err != nil {
			s.logf("kill %s after the resume: %v", name, err)
		}
	}
}

// resumeFailed handles an error from the page on socket. A page that
// was restoring the snapshot and could not is let go, and the container
// stays parked for the next page: the snapshot is as good as it was. It
// reports whether the error was such a page's.
func (s *session) resumeFailed(socket *websocket.Conn, message string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phase != phaseResuming || s.socket != socket {
		return false
	}
	s.socket = nil
	s.setPhase(phaseParked)
	s.logf("a page could not resume the guest, which stays parked: %s", message)
	return true
}

// left is called when the page on socket has gone away. A page that is
// the container takes the container with it. A page that was let go
// after parking does not, and nor does one that left before it had
// restored the snapshot.
func (s *session) left(socket *websocket.Conn) {
	s.mu.Lock()
	if s.socket != socket {
		s.mu.Unlock()
		return
	}
	if s.phase == phaseResuming {
		s.socket = nil
		s.setPhase(phaseParked)
		s.mu.Unlock()
		s.logf("the page went away before the guest resumed, which stays parked")
		return
	}
	s.mu.Unlock()

	s.endPark(parkOutcome{err: errPageLeft})
	close(s.gone)
}

// holdSignal keeps a signal for a container that has no page to take
// it, to be sent to the page that resumes the guest. It reports whether
// the signal was held; if not, a page is there and the caller sends the
// signal.
func (s *session) holdSignal(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phase == phaseLive {
		return false
	}
	s.heldSignals = append(s.heldSignals, name)
	return true
}

// waitLive waits until a page is the container: not while the guest is
// being parked, is parked or is being restored, when what is sent to a
// page would reach a guest that has stopped or none at all.
func (s *session) waitLive() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.phase != phaseLive {
		s.changed.Wait()
	}
}

// parking is the supervisor's side of a container's move: the control
// request that parks the guest, and the snapshot a page stores and the
// next page fetches.
type parking struct {
	conversation *session
	logf         func(format string, args ...any)

	// path is the snapshot's file, and limit the most bytes a page may
	// store there.
	path  string
	limit int64

	// movable says the container's program runs where the guest can keep
	// it (the plan's Movable), and running whether the program has been
	// started.
	movable bool
	running func() bool
}

func newParking(conversation *session, path string, limit int64, logf func(string, ...any)) *parking {
	return &parking{
		conversation: conversation,
		logf:         logf,
		path:         path,
		limit:        limit,
		running:      func() bool { return false },
	}
}

// handle serves one park request on a control connection: the answer is
// "ok" and a line of the page's numbers, or the reason the container is
// not parked.
func (p *parking) handle(connection net.Conn, _ *bufio.Reader, _ []string) {
	connection.SetDeadline(time.Now().Add(parkTimeout + controlTimeout))

	stats, err := p.park()
	if err != nil {
		fmt.Fprintln(connection, err.Error())
		return
	}
	encoded, err := json.Marshal(stats)
	if err != nil {
		fmt.Fprintln(connection, err.Error())
		return
	}
	fmt.Fprintln(connection, answerOK)
	fmt.Fprintln(connection, string(encoded))
}

// park has the page save the guest, if the container is one that can be
// saved.
func (p *parking) park() (Stats, error) {
	if !p.movable {
		return nil, errNotMovable
	}
	if !p.running() {
		return nil, errNotRunning
	}
	return p.conversation.park(parkTimeout)
}

// serveSnapshot is the snapshot on the page's server: a page that was
// asked to park stores the snapshot with PUT, and a page that was told
// to restore fetches it with GET.
func (p *parking) serveSnapshot(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPut:
		if !p.conversation.inPhase(phaseParking) {
			http.Error(w, errSnapshotState.Error(), http.StatusConflict)
			return
		}
		if err := p.store(http.MaxBytesReader(w, r.Body, p.limit)); err != nil {
			p.logf("storing the snapshot: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	case http.MethodGet:
		if !p.conversation.inPhase(phaseResuming) {
			http.Error(w, errSnapshotState.Error(), http.StatusNotFound)
			return
		}
		http.ServeFile(w, r, p.path)

	default:
		http.Error(w, "the snapshot is stored with PUT and fetched with GET", http.StatusMethodNotAllowed)
	}
}

// store writes a snapshot to its file, replacing the file in one step
// so that a snapshot half written is never one a page is given.
func (p *parking) store(body io.Reader) error {
	temporary := p.path + temporarySuffix
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, snapshotMode)
	if err != nil {
		return err
	}
	written, err := io.Copy(file, body)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(temporary)
		return err
	}
	p.logf("snapshot stored: %d bytes", written)
	return os.Rename(temporary, p.path)
}

// discard removes the snapshot, which is stale from the moment a page
// has resumed from it: the guest has run on.
func (p *parking) discard() {
	if err := os.Remove(p.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		p.logf("removing the snapshot: %v", err)
	}
}

// ParkResult is what `tabc park` learns from the supervisor.
type ParkResult struct {
	// Stats are the page's times, in milliseconds, and the snapshot's
	// size, in bytes.
	Stats Stats
}

// Park asks the supervisor listening at path to park the container: the
// page saves the guest, the supervisor keeps the snapshot, and the next
// browser to open the container's address resumes the guest.
func Park(path string) (ParkResult, error) {
	var result ParkResult
	connection, err := dialControl(path)
	if err != nil {
		return result, err
	}
	defer connection.Close()
	connection.SetDeadline(time.Now().Add(parkTimeout + controlTimeout))

	if _, err := fmt.Fprintln(connection, requestPark); err != nil {
		return result, err
	}
	reader := bufio.NewReader(connection)
	if err := readAnswer(reader); err != nil {
		return result, err
	}

	// After the answer, one line of the page's numbers.
	line, err := reader.ReadString('\n')
	if err != nil {
		return result, fmt.Errorf("the supervisor did not say how the park went: %w", err)
	}
	err = json.Unmarshal([]byte(line), &result.Stats)
	return result, err
}
