// Package dockerapi answers the Docker command line. The `docker`
// program does nothing itself: every command is requests to a daemon
// over a socket, and whatever answers on the socket is Docker as far as
// the program can tell. This package answers with tabc's own engine
// (internal/engine), so `docker run`, `docker ps` and `docker compose
// up` start containers in browser tabs with no Docker daemon anywhere.
//
// It is the part of the Docker Engine API those commands use, not the
// whole of it. A request for anything else gets a 404 that names the
// request, and is written to the log, which is how the next gap is
// found.
package dockerapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/fzakaria/tabc/internal/engine"
)

// The API version this daemon claims, the oldest a client may speak,
// and the name and version the daemon gives itself.
const (
	apiVersion    = "1.47"
	minAPIVersion = "1.24"
	serverName    = "tabc"
	osType        = "linux"
	// architecture is the guest's, on every host: what an image has to
	// be built for.
	architecture = "arm64"
)

// versionPrefix is what a client puts before every path: the API
// version it speaks.
var versionPrefix = regexp.MustCompile(`^/v[0-9]+\.[0-9]+`)

// Server is the API in front of one engine.
type Server struct {
	Engine *engine.Engine
	// Version is tabc's own.
	Version string
	// Logf records each request this API has no answer for.
	Logf func(format string, args ...any)
}

// route is one request this API answers: a method, a pattern of path
// segments in which "*" matches one segment and "**" any number, and
// the handler, which is given what the stars matched.
type route struct {
	method  string
	pattern []string
	handle  func(s *Server, w http.ResponseWriter, r *http.Request, args []string)
}

var routes = []route{
	{http.MethodGet, []string{"_ping"}, (*Server).ping},
	{http.MethodHead, []string{"_ping"}, (*Server).ping},
	{http.MethodGet, []string{"version"}, (*Server).version},
	{http.MethodGet, []string{"info"}, (*Server).info},
	{http.MethodGet, []string{"events"}, (*Server).events},

	{http.MethodPost, []string{"images", "create"}, (*Server).imagePull},
	{http.MethodGet, []string{"images", "json"}, (*Server).imageList},
	{http.MethodGet, []string{"images", "**", "json"}, (*Server).imageInspect},

	{http.MethodPost, []string{"containers", "create"}, (*Server).containerCreate},
	{http.MethodGet, []string{"containers", "json"}, (*Server).containerList},
	{http.MethodGet, []string{"containers", "*", "json"}, (*Server).containerInspect},
	{http.MethodPost, []string{"containers", "*", "start"}, (*Server).containerStart},
	{http.MethodPost, []string{"containers", "*", "stop"}, (*Server).containerStop},
	{http.MethodPost, []string{"containers", "*", "restart"}, (*Server).containerRestart},
	{http.MethodPost, []string{"containers", "*", "kill"}, (*Server).containerKill},
	{http.MethodPost, []string{"containers", "*", "wait"}, (*Server).containerWait},
	{http.MethodPost, []string{"containers", "*", "resize"}, (*Server).containerResize},
	{http.MethodPost, []string{"containers", "*", "attach"}, (*Server).containerAttach},
	{http.MethodGet, []string{"containers", "*", "logs"}, (*Server).containerLogs},
	{http.MethodDelete, []string{"containers", "*"}, (*Server).containerRemove},

	{http.MethodPost, []string{"containers", "*", "exec"}, (*Server).execCreate},
	{http.MethodPost, []string{"exec", "*", "start"}, (*Server).execStart},
	{http.MethodPost, []string{"exec", "*", "resize"}, (*Server).execResize},
	{http.MethodGet, []string{"exec", "*", "json"}, (*Server).execInspect},

	{http.MethodGet, []string{"networks"}, (*Server).networkList},
	{http.MethodPost, []string{"networks", "create"}, (*Server).networkCreate},
	{http.MethodGet, []string{"networks", "*"}, (*Server).networkInspect},
	{http.MethodDelete, []string{"networks", "*"}, (*Server).networkRemove},
	{http.MethodPost, []string{"networks", "*", "connect"}, (*Server).networkConnect},
	{http.MethodPost, []string{"networks", "*", "disconnect"}, (*Server).networkDisconnect},

	{http.MethodGet, []string{"volumes"}, (*Server).volumeList},
	{http.MethodPost, []string{"volumes", "create"}, (*Server).volumeCreate},
	{http.MethodGet, []string{"volumes", "*"}, (*Server).volumeInspect},
	{http.MethodDelete, []string{"volumes", "*"}, (*Server).volumeRemove},
}

// match returns what a pattern's stars matched in a path's segments,
// and whether the pattern matched at all.
func match(pattern, segments []string) ([]string, bool) {
	var args []string
	for index, part := range pattern {
		switch part {
		case "**":
			// Everything up to what the rest of the pattern needs.
			rest := len(pattern) - index - 1
			if len(segments) < rest+1 {
				return nil, false
			}
			args = append(args, strings.Join(segments[:len(segments)-rest], "/"))
			segments = segments[len(segments)-rest:]
		case "*":
			if len(segments) == 0 {
				return nil, false
			}
			args = append(args, segments[0])
			segments = segments[1:]
		default:
			if len(segments) == 0 || segments[0] != part {
				return nil, false
			}
			segments = segments[1:]
		}
	}
	return args, len(segments) == 0
}

// ServeHTTP finds the route for a request and runs it.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := versionPrefix.ReplaceAllString(r.URL.Path, "")
	segments := strings.Split(strings.Trim(path, "/"), "/")

	for _, candidate := range routes {
		if candidate.method != r.Method {
			continue
		}
		if args, ok := match(candidate.pattern, segments); ok {
			candidate.handle(s, w, r, args)
			return
		}
	}

	s.logf("no answer for %s %s", r.Method, r.URL.RequestURI())
	writeError(w, http.StatusNotFound, fmt.Errorf("tabc does not implement %s %s", r.Method, path))
}

func (s *Server) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// writeJSON writes a JSON answer with a status.
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}

// writeError writes an error the way Docker's clients read one.
func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"message": err.Error()})
}

// fail writes an engine's error with the status that says what kind of
// error it is.
func fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, engine.ErrNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, engine.ErrConflict):
		writeError(w, http.StatusConflict, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}

// ping is how a client finds out a daemon is there and which API
// version to speak to it.
func (s *Server) ping(w http.ResponseWriter, r *http.Request, _ []string) {
	header := w.Header()
	header.Set("Api-Version", apiVersion)
	header.Set("Ostype", osType)
	header.Set("Docker-Experimental", "false")
	header.Set("Server", serverName+"/"+s.Version)
	header.Set("Content-Type", "text/plain; charset=utf-8")
	if r.Method != http.MethodHead {
		w.Write([]byte("OK"))
	}
}

func (s *Server) version(w http.ResponseWriter, _ *http.Request, _ []string) {
	writeJSON(w, http.StatusOK, map[string]any{
		"Platform":      map[string]string{"Name": serverName},
		"Version":       s.Version,
		"ApiVersion":    apiVersion,
		"MinAPIVersion": minAPIVersion,
		"Os":            osType,
		"Arch":          architecture,
		"KernelVersion": "unkernel",
		"GoVersion":     runtime.Version(),
		"Components": []map[string]any{{
			"Name":    "Engine",
			"Version": s.Version,
			"Details": map[string]string{"ApiVersion": apiVersion, "Os": osType, "Arch": architecture},
		}},
	})
}

func (s *Server) info(w http.ResponseWriter, _ *http.Request, _ []string) {
	containers := s.Engine.Containers()
	running := 0
	for _, container := range containers {
		if container.State().Status == engine.StatusRunning {
			running++
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ID":            serverName,
		"Name":          serverName,
		"ServerVersion": s.Version,
		// Compose reads the daemon's clock to know from when to ask for
		// events.
		"SystemTime":        time.Now().UTC().Format(time.RFC3339Nano),
		"Containers":        len(containers),
		"ContainersRunning": running,
		"ContainersStopped": len(containers) - running,
		"Images":            len(s.Engine.Images()),
		"Driver":            "tar-layers",
		"OperatingSystem":   "tabc (containers in browser tabs)",
		"OSType":            osType,
		"Architecture":      "aarch64",
		"NCPU":              1,
		"DefaultRuntime":    serverName,
		"Runtimes":          map[string]any{serverName: map[string]string{"path": serverName}},
		"Plugins":           map[string]any{"Network": []string{"bridge"}, "Volume": []string{"local"}},
		"SecurityOptions":   []string{},
		"Warnings":          []string{},
	})
}
