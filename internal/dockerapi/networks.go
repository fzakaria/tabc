package dockerapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/fzakaria/tabc/internal/engine"
)

// scopeLocal is the only scope a network or a volume here has, and
// driverLocal the only driver a volume has.
const (
	scopeLocal  = "local"
	driverLocal = "local"
)

// networkJSON returns a network as a list or an inspect answers it,
// with the containers on it.
func (s *Server) networkJSON(network *engine.Network) map[string]any {
	containers := map[string]any{}
	for _, container := range s.Engine.Containers() {
		if _, on := container.Spec.Networks[network.Name]; on {
			containers[container.ID] = map[string]string{"Name": container.Spec.Name}
		}
	}

	return map[string]any{
		"Name":       network.Name,
		"Id":         network.ID,
		"Created":    network.Created.UTC().Format(time.RFC3339Nano),
		"Scope":      scopeLocal,
		"Driver":     network.Driver,
		"EnableIPv6": false,
		"IPAM":       map[string]any{"Driver": "default", "Config": []any{}},
		"Internal":   false,
		"Attachable": false,
		"Ingress":    false,
		"Containers": containers,
		"Options":    map[string]string{},
		"Labels":     orEmpty(network.Labels),
	}
}

// orEmpty returns labels, or an empty set for none: clients read a
// missing set as an error.
func orEmpty(labels map[string]string) map[string]string {
	if labels == nil {
		return map[string]string{}
	}
	return labels
}

func (s *Server) networkList(w http.ResponseWriter, r *http.Request, _ []string) {
	wanted := parseFilters(r.URL.Query().Get(paramFilters))

	list := []map[string]any{}
	for _, network := range s.Engine.Networks() {
		// A name filter matches any part of a name, as Docker's does.
		if !wanted.matchAny(filterName, func(name string) bool { return strings.Contains(network.Name, name) }) {
			continue
		}
		if !wanted.matchAny(filterID, func(id string) bool { return strings.HasPrefix(network.ID, id) }) {
			continue
		}
		if !wanted.matchLabels(network.Labels) {
			continue
		}
		list = append(list, s.networkJSON(network))
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) networkInspect(w http.ResponseWriter, _ *http.Request, args []string) {
	network, err := s.Engine.Network(args[0])
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.networkJSON(network))
}

func (s *Server) networkCreate(w http.ResponseWriter, r *http.Request, _ []string) {
	var request struct {
		Name   string
		Labels map[string]string
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	network, err := s.Engine.CreateNetwork(request.Name, request.Labels)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"Id": network.ID, "Warning": ""})
}

func (s *Server) networkRemove(w http.ResponseWriter, _ *http.Request, args []string) {
	if err := s.Engine.RemoveNetwork(args[0]); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) networkConnect(w http.ResponseWriter, r *http.Request, args []string) {
	var request struct {
		Container      string
		EndpointConfig struct{ Aliases []string }
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.Engine.Connect(args[0], request.Container, request.EndpointConfig.Aliases); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) networkDisconnect(w http.ResponseWriter, r *http.Request, args []string) {
	var request struct{ Container string }
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.Engine.Disconnect(args[0], request.Container); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// volumeJSON returns a volume as a list or an inspect answers it.
func volumeJSON(volume *engine.Volume) map[string]any {
	return map[string]any{
		"Name":       volume.Name,
		"Driver":     driverLocal,
		"Mountpoint": volume.Path,
		"CreatedAt":  volume.Created.UTC().Format(time.RFC3339),
		"Labels":     orEmpty(volume.Labels),
		"Scope":      scopeLocal,
		"Options":    map[string]string{},
	}
}

func (s *Server) volumeList(w http.ResponseWriter, r *http.Request, _ []string) {
	wanted := parseFilters(r.URL.Query().Get(paramFilters))

	list := []map[string]any{}
	for _, volume := range s.Engine.Volumes() {
		if !wanted.matchAny(filterName, func(name string) bool { return strings.Contains(volume.Name, name) }) {
			continue
		}
		if !wanted.matchLabels(volume.Labels) {
			continue
		}
		list = append(list, volumeJSON(volume))
	}
	writeJSON(w, http.StatusOK, map[string]any{"Volumes": list, "Warnings": []string{}})
}

func (s *Server) volumeCreate(w http.ResponseWriter, r *http.Request, _ []string) {
	var request struct {
		Name   string
		Labels map[string]string
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	volume, err := s.Engine.CreateVolume(request.Name, request.Labels)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, volumeJSON(volume))
}

func (s *Server) volumeInspect(w http.ResponseWriter, _ *http.Request, args []string) {
	volume, err := s.Engine.Volume(args[0])
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, volumeJSON(volume))
}

func (s *Server) volumeRemove(w http.ResponseWriter, _ *http.Request, args []string) {
	if err := s.Engine.RemoveVolume(args[0]); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
