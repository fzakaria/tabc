// Package dashboard serves the page that shows every container a
// daemon runs, each in a pane of its own (page/dashboard.html).
//
// In this mode the page is more than a view. A container is a page
// that boots a guest, and no hidden browser is started to load it: the
// frame in the container's pane does. The daemon makes the container's
// supervisor, the dashboard sees a new container in the list with a
// page address, and loading that address in a frame is what brings the
// container up.
package dashboard

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"path"
	"strings"

	"github.com/fzakaria/tabc/internal/assets"
	"github.com/fzakaria/tabc/internal/engine"
)

// tokenBytes is the length of the secret in the dashboard's address.
// The dashboard listens on loopback, where any local process can
// connect, and its list gives away each container's own secret address.
const tokenBytes = 16

// prefix is what the dashboard's address starts with, before the
// secret, and the names of what is served under it.
const (
	prefix    = "/d/"
	listFile  = "containers.json"
	pageFile  = "dashboard.html"
	loopback  = "127.0.0.1:0"
	listenNet = "tcp4"
)

// Server is one daemon's dashboard.
type Server struct {
	engine   *engine.Engine
	share    fs.FS
	token    string
	listener net.Listener
}

// item is one container in the dashboard's list.
type item struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Image    string `json:"image"`
	Status   string `json:"status"`
	ExitCode int    `json:"exitCode"`
	// URL is the container's page, "" until the container is started.
	URL   string `json:"url"`
	Ports []port `json:"ports"`
}

// port is a port of the host a container publishes.
type port struct {
	Host  int `json:"host"`
	Guest int `json:"guest"`
}

// Listen starts a dashboard for an engine on a loopback port the system
// picks. share holds the page (internal/assets).
func Listen(e *engine.Engine, share fs.FS) (*Server, error) {
	secret := make([]byte, tokenBytes)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	listener, err := net.Listen(listenNet, loopback)
	if err != nil {
		return nil, err
	}

	server := &Server{engine: e, share: share, token: hex.EncodeToString(secret), listener: listener}
	go http.Serve(listener, server)
	return server, nil
}

// URL returns the dashboard's address, secret included.
func (s *Server) URL() string {
	return "http://" + s.listener.Addr().String() + prefix + s.token + "/"
}

// Close stops the dashboard.
func (s *Server) Close() error {
	return s.listener.Close()
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, prefix+s.token+"/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")

	switch rest {
	case listFile:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(s.list())
	case "":
		http.ServeFileFS(w, r, s.share, path.Join(assets.Page, pageFile))
	default:
		// The page's own script. A name that climbs out of the page's
		// directory finds nothing: fs.FS refuses one.
		if strings.HasSuffix(rest, ".js") {
			w.Header().Set("Content-Type", "text/javascript")
		}
		http.ServeFileFS(w, r, s.share, path.Join(assets.Page, rest))
	}
}

// list returns the daemon's containers, oldest first, so a pane keeps
// its place as others arrive.
func (s *Server) list() []item {
	containers := s.engine.Containers()
	items := make([]item, 0, len(containers))
	for index := len(containers) - 1; index >= 0; index-- {
		container := containers[index]
		state := container.State()

		entry := item{
			ID:       container.ID,
			Name:     container.Spec.Name,
			Image:    container.Spec.Image,
			Status:   string(state.Status),
			ExitCode: state.ExitCode,
			URL:      s.engine.PageURL(container),
			Ports:    []port{},
		}
		for _, published := range container.Spec.Publish {
			entry.Ports = append(entry.Ports, port{Host: published.HostPort, Guest: published.GuestPort})
		}
		items = append(items, entry)
	}
	return items
}
