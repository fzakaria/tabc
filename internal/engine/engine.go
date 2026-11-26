// Package engine is the part of a container engine tabc has to bring
// itself where there is none: on a Mac, where Docker's daemon does not
// run. It remembers which containers, networks and volumes exist,
// starts a container's supervisor when asked, keeps what the container
// prints, and says what happened to whom.
//
// It knows nothing of HTTP. internal/dockerapi puts Docker's API in
// front of it, so the Docker command line and Compose can drive it.
package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fzakaria/tabc/internal/image"
	"github.com/fzakaria/tabc/internal/plan"
)

// The errors a caller tells apart.
var (
	// ErrNotFound: no such container, image, network or volume.
	ErrNotFound = errors.New("not found")
	// ErrConflict: the name is taken, or the thing is in a state the
	// request cannot be carried out in.
	ErrConflict = errors.New("conflict")
)

// idBytes is the length of an id before it is written as hex: 64
// characters, as Docker's are. shortID is how much of one names a
// container's state directory, and what people type.
const (
	idBytes = 32
	shortID = 12
)

// volumesDir is where named volumes live under the data directory, and
// volumeData the directory of a volume that holds its files, as
// Docker's layout has it.
const (
	volumesDir = "volumes"
	volumeData = "_data"
	volumeMode = 0o755
)

// Config is where an engine keeps things and how it runs containers.
type Config struct {
	// Root is where containers' state directories go.
	Root string
	// CacheDir is where images are cached, and DataDir where volumes
	// are kept.
	CacheDir string
	DataDir  string
	// Browser is who loads a container's page: a headless Chromium per
	// container, or a dashboard page somebody has open.
	Browser plan.Browser
	// Self is the tabc binary supervisors are run from.
	Self string
}

// Engine is the state of one daemon.
type Engine struct {
	config Config

	mu         sync.Mutex
	containers map[string]*Container
	networks   map[string]*Network
	volumes    map[string]*Volume
	// execs are the second programs asked for, by id (execs.go).
	execs map[string]*Exec

	events *events
}

// New returns an engine with Docker's three built-in networks and no
// containers.
func New(config Config) *Engine {
	e := &Engine{
		config:     config,
		containers: map[string]*Container{},
		networks:   map[string]*Network{},
		volumes:    map[string]*Volume{},
		execs:      map[string]*Exec{},
		events:     newEvents(),
	}
	for _, name := range builtinNetworks {
		e.networks[name] = &Network{ID: newID(), Name: name, Driver: name, Created: time.Now(), Builtin: true}
	}
	return e
}

// Browser says who loads a container's page.
func (e *Engine) Browser() plan.Browser {
	return e.config.Browser
}

// newID returns a fresh 64-character id.
func newID() string {
	random := make([]byte, idBytes)
	if _, err := rand.Read(random); err != nil {
		panic(err)
	}
	return hex.EncodeToString(random)
}

// Pull fetches an image from its registry into the cache, naming each
// layer fetched on progress.
func (e *Engine) Pull(ctx context.Context, reference string, progress io.Writer) (*image.Image, error) {
	pulled, err := image.Get(ctx, reference, e.config.CacheDir, image.AlwaysPull, progress)
	if err != nil {
		return nil, err
	}
	e.events.emit(Event{Type: "image", Action: "pull", ID: reference, Attributes: map[string]string{"name": reference}})
	return pulled, nil
}

// Image returns a cached image by reference.
func (e *Engine) Image(reference string) (*image.Image, error) {
	found, ok := image.Lookup(reference, e.config.CacheDir)
	if !ok {
		return nil, fmt.Errorf("%w: no such image: %s", ErrNotFound, reference)
	}
	return found, nil
}

// Images returns every cached image, by name.
func (e *Engine) Images() []*image.Image {
	images := image.List(e.config.CacheDir)
	sort.Slice(images, func(i, j int) bool { return images[i].Reference < images[j].Reference })
	return images
}

// Volume is a named directory that outlives the containers using it.
type Volume struct {
	Name    string
	Path    string
	Labels  map[string]string
	Created time.Time
}

// CreateVolume makes a volume, or returns the one of that name.
func (e *Engine) CreateVolume(name string, labels map[string]string) (*Volume, error) {
	if name == "" {
		name = newID()
	}
	if name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		return nil, fmt.Errorf("invalid volume name %q", name)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if existing, has := e.volumes[name]; has {
		return existing, nil
	}

	path := filepath.Join(e.config.DataDir, volumesDir, name, volumeData)
	if err := os.MkdirAll(path, volumeMode); err != nil {
		return nil, err
	}
	volume := &Volume{Name: name, Path: path, Labels: labels, Created: time.Now()}
	e.volumes[name] = volume
	e.events.emit(Event{Type: "volume", Action: "create", ID: name})
	return volume, nil
}

// Volume returns a volume by name.
func (e *Engine) Volume(name string) (*Volume, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	volume, has := e.volumes[name]
	if !has {
		return nil, fmt.Errorf("%w: no such volume: %s", ErrNotFound, name)
	}
	return volume, nil
}

// Volumes returns every volume, by name.
func (e *Engine) Volumes() []*Volume {
	e.mu.Lock()
	defer e.mu.Unlock()
	volumes := make([]*Volume, 0, len(e.volumes))
	for _, volume := range e.volumes {
		volumes = append(volumes, volume)
	}
	sort.Slice(volumes, func(i, j int) bool { return volumes[i].Name < volumes[j].Name })
	return volumes
}

// RemoveVolume forgets a volume and deletes its files.
func (e *Engine) RemoveVolume(name string) error {
	e.mu.Lock()
	volume, has := e.volumes[name]
	if has {
		delete(e.volumes, name)
	}
	e.mu.Unlock()
	if !has {
		return fmt.Errorf("%w: no such volume: %s", ErrNotFound, name)
	}

	e.events.emit(Event{Type: "volume", Action: "destroy", ID: name})
	return os.RemoveAll(filepath.Dir(volume.Path))
}
