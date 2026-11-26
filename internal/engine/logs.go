package engine

import (
	"sync"
	"time"
)

// The streams a program writes, numbered as Docker's log format numbers
// them.
const (
	StreamStdout byte = 1
	StreamStderr byte = 2
)

// logLimit is the most output a container's log keeps. Older output is
// dropped first: a log is for reading the recent past.
const logLimit = 4 << 20

// Frame is one write of a program to one of its streams.
type Frame struct {
	Stream byte
	Data   []byte
	Time   time.Time
}

// Log is what a container's program has printed, kept for `logs`, and
// handed live to whoever is attached.
type Log struct {
	mu        sync.Mutex
	frames    []Frame
	size      int
	followers map[*follower]bool
	closed    bool
}

// follower is one reader of a log as it grows.
type follower struct {
	frames chan Frame
	gone   chan struct{}
}

func newLog() *Log {
	return &Log{followers: map[*follower]bool{}}
}

// Write adds output to the log and hands it to every follower. A
// follower that reads slowly slows the program down, as a slow terminal
// does. One that has gone is skipped.
func (l *Log) Write(stream byte, data []byte) {
	frame := Frame{Stream: stream, Data: append([]byte(nil), data...), Time: time.Now()}

	l.mu.Lock()
	l.frames = append(l.frames, frame)
	l.size += len(frame.Data)
	for l.size > logLimit && len(l.frames) > 1 {
		l.size -= len(l.frames[0].Data)
		l.frames = l.frames[1:]
	}
	followers := make([]*follower, 0, len(l.followers))
	for reader := range l.followers {
		followers = append(followers, reader)
	}
	l.mu.Unlock()

	for _, reader := range followers {
		select {
		case reader.frames <- frame:
		case <-reader.gone:
		}
	}
}

// Follow returns the log so far, if history is wanted, and a channel of
// what is written from now on, which closes when the log does. cancel
// says the follower has gone.
func (l *Log) Follow(history bool) (past []Frame, live <-chan Frame, cancel func()) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if history {
		past = append(past, l.frames...)
	}
	reader := &follower{frames: make(chan Frame), gone: make(chan struct{})}
	if l.closed {
		close(reader.frames)
		return past, reader.frames, func() {}
	}
	l.followers[reader] = true

	var once sync.Once
	return past, reader.frames, func() {
		once.Do(func() {
			close(reader.gone)
			l.mu.Lock()
			delete(l.followers, reader)
			l.mu.Unlock()
		})
	}
}

// Close ends the log: the program has exited and nothing more will be
// written. Followers' channels close once they have read what was sent.
func (l *Log) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	l.closed = true
	for reader := range l.followers {
		close(reader.frames)
		delete(l.followers, reader)
	}
}

// Reopen makes a closed log writable again, for a container started a
// second time. What the earlier run printed stays.
func (l *Log) Reopen() {
	l.mu.Lock()
	l.closed = false
	l.mu.Unlock()
}
