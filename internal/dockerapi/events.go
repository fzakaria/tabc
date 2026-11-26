package dockerapi

import (
	"encoding/json"
	"net/http"

	"github.com/fzakaria/tabc/internal/engine"
)

// events streams what happens to containers, networks, volumes and
// images, one JSON object a line, until the client goes.
func (s *Server) events(w http.ResponseWriter, r *http.Request, _ []string) {
	wanted := parseFilters(r.URL.Query().Get(paramFilters))
	happenings, stop := s.Engine.Events()
	defer stop()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}
	encoder := json.NewEncoder(w)

	for {
		select {
		case <-r.Context().Done():
			return
		case event := <-happenings:
			if !eventWanted(wanted, event) {
				continue
			}
			encoder.Encode(map[string]any{
				"Type":     event.Type,
				"Action":   event.Action,
				"Actor":    map[string]any{"ID": event.ID, "Attributes": event.Attributes},
				"scope":    scopeLocal,
				"time":     event.Time.Unix(),
				"timeNano": event.Time.UnixNano(),
				// The same again under the names older clients read.
				"status": event.Action,
				"id":     event.ID,
				"from":   event.Attributes["image"],
			})
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

// eventWanted reports whether an event passes a client's filters: its
// type, its action, the container it is about, and the labels that
// container carries.
func eventWanted(wanted filters, event engine.Event) bool {
	if !wanted.matchAny(filterType, func(kind string) bool { return kind == event.Type }) {
		return false
	}
	if !wanted.matchAny(filterEvent, func(action string) bool { return action == event.Action }) {
		return false
	}
	if !wanted.matchAny(filterCont, func(id string) bool { return id == event.ID || id == event.Attributes["name"] }) {
		return false
	}
	return wanted.matchLabels(event.Attributes)
}
