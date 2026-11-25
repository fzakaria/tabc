package supervisor

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/fzakaria/tabc/internal/guest"
)

// tokenBytes is the length of the secret in the page's URL. The server
// listens on loopback, where any local process can connect, and only a
// request that carries the secret is answered.
const tokenBytes = 16

// prefix is what every URL starts with, before the secret.
const prefix = "/t/"

// What is served under the secret, by the first element of the rest of
// the path. Anything else is a file of the page.
const (
	routeConfig   = "config.json"
	routeSpec     = "spec.json"
	routeInit     = "init"
	routeFS       = "fs"
	routeControl  = "ctl"
	routeNet      = "net"
	routeUnkernel = "unkernel"
	netLookup     = "dns"
	netConnect    = "tcp"
)

// Where the share directory keeps the page, the unkernel SDK with its
// kernel, and the guest's init.
const (
	sharePage     = "page"
	shareUnkernel = "unkernel"
	shareInit     = "init"
	indexFile     = "index.html"
)

// pageConfig is what the page needs to know before it boots the guest.
type pageConfig struct {
	Terminal  bool   `json:"terminal"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
	Term      string `json:"term"`
	MemoryMiB int    `json:"memoryMiB"`
	Network   bool   `json:"network"`
}

// fileServer answers the page's requests for the container's files
// (internal/rootfs).
type fileServer interface {
	Serve(w http.ResponseWriter, r *http.Request, op string)
}

// server is the HTTP server one container's page talks to.
type server struct {
	token    string
	shareDir string
	config   pageConfig
	spec     guest.Spec
	files    fileServer
	session  *session
	resolver *net.Resolver
}

// newToken returns a fresh secret.
func newToken() (string, error) {
	secret := make([]byte, tokenBytes)
	if _, err := rand.Read(secret); err != nil {
		return "", err
	}
	return hex.EncodeToString(secret), nil
}

// pageURL returns the address of the page on a listener.
func (s *server) pageURL(listener net.Listener) string {
	return "http://" + listener.Addr().String() + prefix + s.token + "/"
}

// ServeHTTP routes one request. Every answer carries the headers that
// isolate the page from other origins, which is what lets the SDK share
// memory with its workers and keeps the page in a process of its own.
func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, prefix+s.token+"/")
	if !ok {
		http.NotFound(w, r)
		return
	}

	header := w.Header()
	header.Set("Cross-Origin-Opener-Policy", "same-origin")
	header.Set("Cross-Origin-Embedder-Policy", "require-corp")
	header.Set("Cross-Origin-Resource-Policy", "same-origin")
	header.Set("Cache-Control", "no-store")

	first, remainder, _ := strings.Cut(rest, "/")
	switch first {
	case routeConfig:
		writeJSON(w, s.config)
	case routeSpec:
		writeJSON(w, s.spec)
	case routeInit:
		http.ServeFile(w, r, filepath.Join(s.shareDir, shareInit))
	case routeFS:
		s.files.Serve(w, r, remainder)
	case routeControl:
		s.session.serve(w, r)
	case routeNet:
		s.serveNet(w, r, remainder)
	case routeUnkernel:
		serveShared(w, r, filepath.Join(s.shareDir, shareUnkernel), remainder)
	default:
		if rest == "" {
			rest = indexFile
		}
		serveShared(w, r, filepath.Join(s.shareDir, sharePage), rest)
	}
}

// serveNet routes the guest's lookups and connections, when the
// container has a network.
func (s *server) serveNet(w http.ResponseWriter, r *http.Request, what string) {
	if !s.config.Network {
		http.NotFound(w, r)
		return
	}
	switch what {
	case netLookup:
		s.serveLookup(w, r)
	case netConnect:
		s.serveConnection(w, r)
	default:
		http.NotFound(w, r)
	}
}

// serveShared serves one file from a directory of the share. The name
// comes from the page, so a name that climbs out of the directory is
// refused.
func serveShared(w http.ResponseWriter, r *http.Request, dir, name string) {
	if !filepath.IsLocal(name) {
		http.NotFound(w, r)
		return
	}

	// Browsers load a module or a wasm file only under its own type,
	// and the host's MIME table is not to be relied on for either.
	switch filepath.Ext(name) {
	case ".js", ".mjs":
		w.Header().Set("Content-Type", "text/javascript")
	case ".wasm":
		w.Header().Set("Content-Type", "application/wasm")
	}
	http.ServeFile(w, r, filepath.Join(dir, name))
}

// writeJSON writes a JSON answer.
func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(value)
}
