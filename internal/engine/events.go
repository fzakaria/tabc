package engine

import (
	"sync"
	"time"
)

// eventBacklog is how many events a listener may fall behind by before
// events are dropped for that listener. An event is news, not a record:
// a listener that cannot keep up asks for the state itself.
const eventBacklog = 256

// Event is something that happened to a container, a network, a volume
// or an image, in the words Docker's event stream uses.
type Event struct {
	Type       string
	Action     string
	ID         string
	Attributes map[string]string
	Time       time.Time
}

// events hands each event to whoever is listening.
type events struct {
	mu        sync.Mutex
	listeners map[chan Event]bool
}

func newEvents() *events {
	return &events{listeners: map[chan Event]bool{}}
}

// emit sends an event to every listener that has room for it.
func (e *events) emit(event Event) {
	event.Time = time.Now()

	e.mu.Lock()
	defer e.mu.Unlock()
	for listener := range e.listeners {
		select {
		case listener <- event:
		default:
		}
	}
}

// Events returns a channel of everything that happens from now on, and
// the function that stops it.
func (e *Engine) Events() (<-chan Event, func()) {
	listener := make(chan Event, eventBacklog)

	e.events.mu.Lock()
	e.events.listeners[listener] = true
	e.events.mu.Unlock()

	return listener, func() {
		e.events.mu.Lock()
		delete(e.events.listeners, listener)
		e.events.mu.Unlock()
	}
}
