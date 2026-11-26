package dockerapi

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/fzakaria/tabc/internal/engine"
	"github.com/fzakaria/tabc/internal/plan"
)

// stringList is a list of words that a client may send as a list or as
// one string, which is then a single word.
type stringList []string

func (l *stringList) UnmarshalJSON(data []byte) error {
	var words []string
	if err := json.Unmarshal(data, &words); err == nil {
		*l = words
		return nil
	}
	var word string
	if err := json.Unmarshal(data, &word); err != nil {
		return err
	}
	*l = []string{word}
	return nil
}

// portBinding is where on the host a container's port is published.
type portBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string
}

// createRequest is the part of a container's description this API acts
// on.
type createRequest struct {
	Image      string
	Cmd        stringList
	Entrypoint *stringList
	Env        []string
	WorkingDir string
	User       string
	Labels     map[string]string
	Tty        bool
	OpenStdin  bool
	StdinOnce  bool

	HostConfig struct {
		Binds        []string
		NetworkMode  string
		PortBindings map[string][]portBinding
		AutoRemove   bool
		Memory       int64
		Mounts       []struct {
			Type     string
			Source   string
			Target   string
			ReadOnly bool
		}
	}
	NetworkingConfig struct {
		EndpointsConfig map[string]struct{ Aliases []string }
	}
}

// The network modes that name no network of the engine's, the option
// that makes a bind read-only, and what follows a port's number in a
// binding's key.
const (
	networkDefault = "default"
	optionReadOnly = "ro"
	portProtocol   = "/tcp"
)

// spec turns a request into the engine's description of a container.
func (request createRequest) spec(name string) (engine.Spec, error) {
	spec := engine.Spec{
		Name:        name,
		Image:       request.Image,
		Cmd:         request.Cmd,
		Env:         request.Env,
		WorkingDir:  request.WorkingDir,
		User:        request.User,
		Labels:      request.Labels,
		Tty:         request.Tty,
		OpenStdin:   request.OpenStdin,
		StdinOnce:   request.StdinOnce,
		MemoryBytes: request.HostConfig.Memory,
		AutoRemove:  request.HostConfig.AutoRemove,
		Networks:    map[string][]string{},
	}
	if request.Entrypoint != nil {
		spec.Entrypoint = *request.Entrypoint
		spec.ReplaceEntrypoint = true
	}

	// The networks: the mode names the first, and the endpoints the
	// names the container goes by on each.
	if mode := request.HostConfig.NetworkMode; mode != "" && mode != networkDefault {
		spec.Networks[mode] = nil
	}
	for network, endpoint := range request.NetworkingConfig.EndpointsConfig {
		if network == networkDefault {
			continue
		}
		spec.Networks[network] = endpoint.Aliases
	}

	// The published ports. UDP is not carried, so a UDP port is left out.
	for key, bindings := range request.HostConfig.PortBindings {
		guestText, isTCP := strings.CutSuffix(key, portProtocol)
		if !isTCP {
			continue
		}
		guestPort, err := strconv.Atoi(guestText)
		if err != nil {
			return spec, fmt.Errorf("port %q is not a port", key)
		}
		for _, binding := range bindings {
			hostPort := guestPort
			if binding.HostPort != "" {
				if hostPort, err = strconv.Atoi(binding.HostPort); err != nil {
					return spec, fmt.Errorf("host port %q is not a port", binding.HostPort)
				}
			}
			spec.Publish = append(spec.Publish, plan.Publish{HostIP: binding.HostIP, HostPort: hostPort, GuestPort: guestPort})
		}
	}

	// The mounts, in both of the forms clients send them.
	for _, bind := range request.HostConfig.Binds {
		parts := strings.Split(bind, ":")
		if len(parts) < 2 {
			return spec, fmt.Errorf("bind %q is not source:target", bind)
		}
		spec.Mounts = append(spec.Mounts, engine.Mount{
			Source:   parts[0],
			Target:   parts[1],
			ReadOnly: len(parts) > 2 && strings.Contains(parts[2], optionReadOnly),
		})
	}
	for _, mount := range request.HostConfig.Mounts {
		spec.Mounts = append(spec.Mounts, engine.Mount{Source: mount.Source, Target: mount.Target, ReadOnly: mount.ReadOnly})
	}
	return spec, nil
}

func (s *Server) containerCreate(w http.ResponseWriter, r *http.Request, _ []string) {
	var request createRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	spec, err := request.spec(r.URL.Query().Get("name"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	container, err := s.Engine.Create(spec)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"Id": container.ID, "Warnings": []string{}})
}

// statusText describes a container's condition the way `docker ps`
// shows it.
func statusText(state engine.State) string {
	switch state.Status {
	case engine.StatusRunning:
		return "Up " + ago(state.StartedAt)
	case engine.StatusExited:
		return fmt.Sprintf("Exited (%d) %s ago", state.ExitCode, ago(state.FinishedAt))
	default:
		return "Created"
	}
}

// ago says how long it has been since a moment, roughly.
func ago(moment time.Time) string {
	elapsed := time.Since(moment)
	switch {
	case elapsed < time.Minute:
		return fmt.Sprintf("%d seconds", int(elapsed.Seconds()))
	case elapsed < time.Hour:
		return fmt.Sprintf("%d minutes", int(elapsed.Minutes()))
	default:
		return fmt.Sprintf("%d hours", int(elapsed.Hours()))
	}
}

// networksJSON returns the networks a container is on as a list and an
// inspect both answer them.
func (s *Server) networksJSON(container *engine.Container) map[string]any {
	networks := map[string]any{}
	for name, aliases := range container.Spec.Networks {
		id := ""
		if network, err := s.Engine.Network(name); err == nil {
			id = network.ID
		}
		networks[name] = map[string]any{
			"NetworkID":  id,
			"EndpointID": container.ID,
			"Aliases":    aliases,
			"IPAddress":  "",
			"Gateway":    "",
		}
	}
	return networks
}

// timeText writes a moment as Docker does, and a moment that has not
// come as the zero time.
func timeText(moment time.Time) string {
	return moment.UTC().Format(time.RFC3339Nano)
}

func (s *Server) containerList(w http.ResponseWriter, r *http.Request, _ []string) {
	query := r.URL.Query()
	wanted := parseFilters(query.Get(paramFilters))
	all := query.Get("all") == "1" || query.Get("all") == "true"

	list := []map[string]any{}
	for _, container := range s.Engine.Containers() {
		state := container.State()
		spec := container.Spec

		// Without all, or a status asked for, only what is running.
		_, byStatus := wanted[filterStatus]
		if !all && !byStatus && state.Status != engine.StatusRunning {
			continue
		}
		if !wanted.matchAny(filterStatus, func(status string) bool { return string(state.Status) == status }) {
			continue
		}
		if !wanted.matchAny(filterName, func(name string) bool { return strings.Contains(spec.Name, strings.TrimPrefix(name, "/")) }) {
			continue
		}
		if !wanted.matchAny(filterID, func(id string) bool { return strings.HasPrefix(container.ID, id) }) {
			continue
		}
		if !wanted.matchLabels(spec.Labels) {
			continue
		}

		ports := []map[string]any{}
		for _, published := range spec.Publish {
			ports = append(ports, map[string]any{
				"IP":          orDefault(published.HostIP, "127.0.0.1"),
				"PrivatePort": published.GuestPort,
				"PublicPort":  published.HostPort,
				"Type":        "tcp",
			})
		}
		list = append(list, map[string]any{
			"Id":              container.ID,
			"Names":           []string{"/" + spec.Name},
			"Image":           spec.Image,
			"ImageID":         s.imageIDOf(spec.Image),
			"Command":         strings.Join(append(append([]string{}, spec.Entrypoint...), spec.Cmd...), " "),
			"Created":         container.Created.Unix(),
			"State":           state.Status,
			"Status":          statusText(state),
			"Ports":           ports,
			"Labels":          orEmpty(spec.Labels),
			"NetworkSettings": map[string]any{"Networks": s.networksJSON(container)},
			"Mounts":          []any{},
		})
	}
	writeJSON(w, http.StatusOK, list)
}

// orDefault returns text unless it is empty.
func orDefault(text, fallback string) string {
	if text == "" {
		return fallback
	}
	return text
}

// imageIDOf returns the id of the image a container was made from.
func (s *Server) imageIDOf(reference string) string {
	found, err := s.Engine.Image(reference)
	if err != nil {
		return ""
	}
	return imageID(found)
}

func (s *Server) containerInspect(w http.ResponseWriter, _ *http.Request, args []string) {
	container, err := s.Engine.Container(args[0])
	if err != nil {
		fail(w, err)
		return
	}
	state := container.State()
	spec := container.Spec

	ports := map[string]any{}
	bindings := map[string]any{}
	for _, published := range spec.Publish {
		key := strconv.Itoa(published.GuestPort) + portProtocol
		binding := []map[string]string{{
			"HostIp":   orDefault(published.HostIP, "127.0.0.1"),
			"HostPort": strconv.Itoa(published.HostPort),
		}}
		ports[key] = binding
		bindings[key] = binding
	}
	networkMode := networkDefault
	for name := range spec.Networks {
		networkMode = name
		break
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"Id":      container.ID,
		"Created": timeText(container.Created),
		"Path":    strings.Join(spec.Entrypoint, " "),
		"Args":    spec.Cmd,
		"State": map[string]any{
			"Status":     state.Status,
			"Running":    state.Status == engine.StatusRunning,
			"Paused":     false,
			"Restarting": false,
			"OOMKilled":  false,
			"Dead":       false,
			"Pid":        state.Pid,
			"ExitCode":   state.ExitCode,
			"Error":      state.Error,
			"StartedAt":  timeText(state.StartedAt),
			"FinishedAt": timeText(state.FinishedAt),
		},
		"Image":        s.imageIDOf(spec.Image),
		"Name":         "/" + spec.Name,
		"RestartCount": 0,
		"Driver":       "tar-layers",
		"Platform":     osType,
		"Mounts":       []any{},
		"HostConfig": map[string]any{
			"NetworkMode":   networkMode,
			"PortBindings":  bindings,
			"AutoRemove":    spec.AutoRemove,
			"Memory":        spec.MemoryBytes,
			"RestartPolicy": map[string]any{"Name": "no", "MaximumRetryCount": 0},
			"LogConfig":     map[string]any{"Type": "json-file", "Config": map[string]string{}},
		},
		"Config": map[string]any{
			"Hostname":     container.ShortID(),
			"Image":        spec.Image,
			"Cmd":          spec.Cmd,
			"Entrypoint":   spec.Entrypoint,
			"Env":          spec.Env,
			"WorkingDir":   spec.WorkingDir,
			"User":         spec.User,
			"Labels":       orEmpty(spec.Labels),
			"Tty":          spec.Tty,
			"OpenStdin":    spec.OpenStdin,
			"StdinOnce":    spec.StdinOnce,
			"AttachStdout": true,
			"AttachStderr": true,
		},
		"NetworkSettings": map[string]any{"Ports": ports, "Networks": s.networksJSON(container)},
	})
}

func (s *Server) containerStart(w http.ResponseWriter, _ *http.Request, args []string) {
	if err := s.Engine.Start(args[0]); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// defaultStopSeconds is how long a container gets to stop when the
// request does not say.
const defaultStopSeconds = 10

func (s *Server) containerStop(w http.ResponseWriter, r *http.Request, args []string) {
	seconds := defaultStopSeconds
	if given, err := strconv.Atoi(r.URL.Query().Get("t")); err == nil && given >= 0 {
		seconds = given
	}
	if err := s.Engine.Stop(args[0], time.Duration(seconds)*time.Second); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) containerRestart(w http.ResponseWriter, r *http.Request, args []string) {
	if err := s.Engine.Stop(args[0], defaultStopSeconds*time.Second); err != nil {
		fail(w, err)
		return
	}
	s.containerStart(w, r, args)
}

func (s *Server) containerKill(w http.ResponseWriter, r *http.Request, args []string) {
	if err := s.Engine.Kill(args[0], r.URL.Query().Get("signal")); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) containerRemove(w http.ResponseWriter, r *http.Request, args []string) {
	force := r.URL.Query().Get("force")
	if err := s.Engine.Remove(args[0], force == "1" || force == "true"); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) containerResize(w http.ResponseWriter, r *http.Request, args []string) {
	query := r.URL.Query()
	rows, _ := strconv.Atoi(query.Get("h"))
	cols, _ := strconv.Atoi(query.Get("w"))
	if rows > 0 && cols > 0 {
		s.Engine.Resize(args[0], cols, rows)
	}
	w.WriteHeader(http.StatusOK)
}

// The conditions a wait may be for.
const (
	conditionRemoved    = "removed"
	conditionNotRunning = "not-running"
)

// containerWait answers when a container's run ends. The answer's
// status is sent at once and its body when there is something to say,
// which is how a client knows the wait has begun before it starts the
// container.
func (s *Server) containerWait(w http.ResponseWriter, r *http.Request, args []string) {
	container, err := s.Engine.Container(args[0])
	if err != nil {
		fail(w, err)
		return
	}
	condition := r.URL.Query().Get("condition")
	done := container.Done()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}

	// A container that is not running satisfies "not running" already.
	state := container.State()
	waitForRun := !(condition == conditionNotRunning || condition == "") || state.Status == engine.StatusRunning
	if waitForRun {
		select {
		case <-done:
		case <-r.Context().Done():
			return
		}
	}
	if condition == conditionRemoved {
		select {
		case <-container.Removed():
		case <-r.Context().Done():
			return
		}
	}
	json.NewEncoder(w).Encode(map[string]int{"StatusCode": container.State().ExitCode})
}

// The header Docker puts before each piece of a program's output when
// the program has no terminal: the stream's number, three zero bytes,
// and the piece's length.
const (
	frameHeaderSize  = 8
	frameLengthStart = 4
)

// writeFrame writes one piece of output: as it is for a terminal, and
// behind a header that says which stream otherwise.
func writeFrame(w io.Writer, frame engine.Frame, tty bool) error {
	if tty {
		_, err := w.Write(frame.Data)
		return err
	}
	header := make([]byte, frameHeaderSize)
	header[0] = frame.Stream
	binary.BigEndian.PutUint32(header[frameLengthStart:], uint32(len(frame.Data)))
	if _, err := w.Write(header); err != nil {
		return err
	}
	_, err := w.Write(frame.Data)
	return err
}

// The content types of a program's output, with a terminal and without.
const (
	typeRawStream   = "application/vnd.docker.raw-stream"
	typeMultiplexed = "application/vnd.docker.multiplexed-stream"
)

// streamType returns the content type of a container's output.
func streamType(tty bool) string {
	if tty {
		return typeRawStream
	}
	return typeMultiplexed
}

// flagSet reports whether a query parameter is set to true.
func flagSet(r *http.Request, name string) bool {
	value := r.URL.Query().Get(name)
	return value == "1" || value == "true"
}

func (s *Server) containerLogs(w http.ResponseWriter, r *http.Request, args []string) {
	container, err := s.Engine.Container(args[0])
	if err != nil {
		fail(w, err)
		return
	}
	tty := container.Spec.Tty
	follow := flagSet(r, "follow")
	wantOut, wantErr := flagSet(r, "stdout"), flagSet(r, "stderr")

	past, live, cancel := container.Log.Follow(true)
	defer cancel()

	w.Header().Set("Content-Type", streamType(tty))
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	send := func(frame engine.Frame) error {
		if (frame.Stream == engine.StreamStdout && !wantOut) || (frame.Stream == engine.StreamStderr && !wantErr) {
			return nil
		}
		if err := writeFrame(w, frame, tty); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}

	for _, frame := range past {
		if send(frame) != nil {
			return
		}
	}
	if !follow {
		return
	}
	for {
		select {
		case frame, open := <-live:
			if !open || send(frame) != nil {
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}

// containerAttach connects a client to a container's program: the
// program's output to the client, and the client's input to the
// program. The connection stops being HTTP after the first answer, so
// the server takes the raw connection over.
func (s *Server) containerAttach(w http.ResponseWriter, r *http.Request, args []string) {
	container, err := s.Engine.Container(args[0])
	if err != nil {
		fail(w, err)
		return
	}
	tty := container.Spec.Tty

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("this connection cannot be attached"))
		return
	}
	connection, buffered, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer connection.Close()

	// Following starts before the answer, so nothing printed between the
	// two is missed.
	past, live, cancel := container.Log.Follow(flagSet(r, "logs"))
	defer cancel()

	// A client that asked to upgrade is told the upgrade happened; an
	// older one is simply answered.
	if r.Header.Get("Upgrade") != "" {
		fmt.Fprintf(connection, "HTTP/1.1 101 UPGRADED\r\nContent-Type: %s\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n", streamType(tty))
	} else {
		fmt.Fprintf(connection, "HTTP/1.1 200 OK\r\nContent-Type: %s\r\n\r\n", streamType(tty))
	}

	// The client's input, which may have begun arriving with the
	// request.
	if flagSet(r, "stdin") && container.Spec.OpenStdin {
		go s.Engine.WriteInput(container, buffered.Reader)
	}

	for _, frame := range past {
		if writeFrame(connection, frame, tty) != nil {
			return
		}
	}
	if !flagSet(r, "stream") {
		return
	}
	for frame := range live {
		if writeFrame(connection, frame, tty) != nil {
			return
		}
	}

	// The program is done. Its output's end is said by closing the
	// sending side, so a client still reading sees an end and not a
	// reset.
	if closer, ok := connection.(interface{ CloseWrite() error }); ok {
		closer.CloseWrite()
	}
}
