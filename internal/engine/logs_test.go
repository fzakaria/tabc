package engine

import (
	"testing"
	"time"
)

// TestLogFollow checks that a follower who asks for history gets what
// was written before and then what is written after, in order, and that
// the channel closes when the log does.
func TestLogFollow(t *testing.T) {
	log := newLog()
	log.Write(StreamStdout, []byte("before"))

	past, live, cancel := log.Follow(true)
	defer cancel()
	if len(past) != 1 || string(past[0].Data) != "before" {
		t.Fatalf("history: %v", past)
	}

	go func() {
		log.Write(StreamStderr, []byte("after"))
		log.Close()
	}()
	frame, open := <-live
	if !open || string(frame.Data) != "after" || frame.Stream != StreamStderr {
		t.Errorf("live: %q on %d, open %v", frame.Data, frame.Stream, open)
	}
	if _, open := <-live; open {
		t.Error("the channel stayed open after the log closed")
	}
}

// TestLogGoneFollower checks that a follower who stops reading and says
// so does not block the program: a write after the cancel returns.
func TestLogGoneFollower(t *testing.T) {
	log := newLog()
	_, _, cancel := log.Follow(false)
	cancel()

	written := make(chan struct{})
	go func() {
		log.Write(StreamStdout, []byte("x"))
		close(written)
	}()
	select {
	case <-written:
	case <-time.After(2 * time.Second):
		t.Fatal("a write blocked on a follower that had gone")
	}
}

// TestLogLimit checks that the log drops its oldest output once it
// holds more than its limit, and keeps the newest.
func TestLogLimit(t *testing.T) {
	log := newLog()
	chunk := make([]byte, logLimit/4)
	for range 6 {
		log.Write(StreamStdout, chunk)
	}
	log.Write(StreamStdout, []byte("newest"))

	past, _, cancel := log.Follow(true)
	defer cancel()
	total := 0
	for _, frame := range past {
		total += len(frame.Data)
	}
	if total > logLimit {
		t.Errorf("the log holds %d bytes, over its limit of %d", total, logLimit)
	}
	if string(past[len(past)-1].Data) != "newest" {
		t.Error("the newest output was dropped")
	}
}
