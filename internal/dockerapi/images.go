package dockerapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/fzakaria/tabc/internal/image"
)

// The parameters of a pull: the image and its tag.
const (
	paramFromImage = "fromImage"
	paramTag       = "tag"
	defaultTag     = "latest"
)

// hashPrefix starts an id that is a hash, and the names the cache's
// full references are shortened by for showing, as Docker shortens
// them.
const (
	hashPrefix       = "sha256:"
	defaultRegistry  = "index.docker.io/"
	defaultNamespace = "library/"
)

// progressWriter turns the lines an image pull prints into the JSON
// messages a Docker client shows as progress.
type progressWriter struct {
	encoder *json.Encoder
	flusher http.Flusher
}

func (p progressWriter) Write(line []byte) (int, error) {
	p.status(strings.TrimSpace(string(line)))
	return len(line), nil
}

func (p progressWriter) status(text string) {
	p.encoder.Encode(map[string]string{"status": text})
	if p.flusher != nil {
		p.flusher.Flush()
	}
}

// imagePull fetches an image, streaming what happens.
func (s *Server) imagePull(w http.ResponseWriter, r *http.Request, _ []string) {
	query := r.URL.Query()
	reference := query.Get(paramFromImage)
	if reference == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("tabc pulls images and does not import them: %s is required", paramFromImage))
		return
	}

	// A tag may be a name or a digest, and a reference may carry its own.
	tag := query.Get(paramTag)
	switch {
	case strings.HasPrefix(tag, hashPrefix):
		reference += "@" + tag
	case tag != "":
		reference += ":" + tag
	case !strings.Contains(lastSegment(reference), ":") && !strings.Contains(reference, "@"):
		reference += ":" + defaultTag
	}

	// The status is sent before the pull, so a failure has to travel in
	// the stream, as Docker's does.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	progress := progressWriter{encoder: json.NewEncoder(w), flusher: flusher}

	progress.status("Pulling " + reference)
	if _, err := s.Engine.Pull(r.Context(), reference, progress); err != nil {
		progress.encoder.Encode(map[string]any{
			"error":       err.Error(),
			"errorDetail": map[string]string{"message": err.Error()},
		})
		return
	}
	progress.status("Status: Downloaded newer image for " + reference)
}

// lastSegment returns what follows the last slash of a reference, which
// is where a tag is.
func lastSegment(reference string) string {
	return reference[strings.LastIndex(reference, "/")+1:]
}

// shortName returns a full reference as Docker shows it: without the
// default registry and namespace.
func shortName(reference string) string {
	reference = strings.TrimPrefix(reference, defaultRegistry)
	return strings.TrimPrefix(reference, defaultNamespace)
}

// imageID returns an image's id, a hash. An image cached before ids
// were recorded gets one made of its name.
func imageID(found *image.Image) string {
	if found.ID != "" {
		return found.ID
	}
	return hashPrefix + fmt.Sprintf("%064x", []byte(found.Reference))[:64]
}

// imageJSON returns an image as an inspect answers it.
func imageJSON(found *image.Image) map[string]any {
	config := found.Config
	return map[string]any{
		"Id":           imageID(found),
		"RepoTags":     []string{shortName(found.Reference)},
		"RepoDigests":  []string{},
		"Created":      time.Unix(0, 0).UTC().Format(time.RFC3339),
		"Architecture": architecture,
		"Os":           osType,
		"Size":         0,
		"Config": map[string]any{
			"Env":          config.Env,
			"Cmd":          config.Cmd,
			"Entrypoint":   config.Entrypoint,
			"WorkingDir":   config.WorkingDir,
			"User":         config.User,
			"Labels":       config.Labels,
			"ExposedPorts": config.ExposedPorts,
			"Volumes":      config.Volumes,
		},
		"RootFS": map[string]any{"Type": "layers", "Layers": []string{}},
	}
}

func (s *Server) imageInspect(w http.ResponseWriter, _ *http.Request, args []string) {
	found, err := s.Engine.Image(args[0])
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, imageJSON(found))
}

func (s *Server) imageList(w http.ResponseWriter, _ *http.Request, _ []string) {
	list := []map[string]any{}
	for _, found := range s.Engine.Images() {
		list = append(list, map[string]any{
			"Id":          imageID(found),
			"RepoTags":    []string{shortName(found.Reference)},
			"RepoDigests": []string{},
			"Created":     0,
			"Size":        0,
			"Containers":  -1,
			"Labels":      found.Config.Labels,
		})
	}
	writeJSON(w, http.StatusOK, list)
}
