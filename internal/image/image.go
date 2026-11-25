// Package image fetches a container image from a registry for `tabc
// run`, which has no engine to do that for it. The layers are kept as
// uncompressed tar files in a cache and never unpacked: the root file
// system is read out of them in place (internal/rootfs, LayerTree).
package image

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// The guest is aarch64 Linux on every host, so that is the platform of
// every image, whatever machine asks.
const (
	guestOS   = "linux"
	guestArch = "arm64"
)

// Where the cache keeps layers and what each reference last resolved to
// under its root, a layer's file name, and the name a layer has while
// it downloads.
const (
	layersDir     = "layers"
	imagesDir     = "images"
	recordSuffix  = ".json"
	recordMode    = 0o644
	layerSuffix   = ".tar"
	partialSuffix = ".partial"
	cacheDirMode  = 0o755
	cacheDirName  = "tabc"
)

// Image is a pulled image: where its layers are, lowest first, and the
// configuration its author gave it.
type Image struct {
	Layers []string
	Config v1.Config
}

// CacheDir returns the directory layers are cached in, under the
// user's cache directory.
func CacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, cacheDirName), nil
}

// Refresh says whether Get asks the registry about an image the cache
// already has.
type Refresh int

const (
	// UseCached runs the cached image without asking, as an engine runs
	// an image it has pulled.
	UseCached Refresh = iota
	// AlwaysPull asks the registry what the reference names now.
	AlwaysPull
)

// Get returns the image a reference names, such as "alpine" or
// "ghcr.io/owner/name:tag": from the cache under cacheDir when the
// cache has it and refresh allows, else from its registry.
func Get(ctx context.Context, reference, cacheDir string, refresh Refresh, progress io.Writer) (*Image, error) {
	parsed, err := name.ParseReference(reference)
	if err != nil {
		return nil, fmt.Errorf("image %q: %w", reference, err)
	}

	record := filepath.Join(cacheDir, imagesDir, recordName(parsed.Name()))
	if refresh == UseCached {
		if cached, ok := loadRecord(record); ok {
			return cached, nil
		}
	}

	image, err := pull(ctx, parsed, cacheDir, progress)
	if err != nil {
		return nil, err
	}
	saveRecord(record, image)
	return image, nil
}

// recordName is the file an image's record is kept in: the hash of the
// reference's full name, which may hold characters a file name cannot.
func recordName(fullName string) string {
	sum := sha256.Sum256([]byte(fullName))
	return hex.EncodeToString(sum[:]) + recordSuffix
}

// loadRecord returns the image a record describes, if the record is
// there and every layer the record names still is.
func loadRecord(path string) (*Image, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var image Image
	if err := json.Unmarshal(data, &image); err != nil {
		return nil, false
	}
	for _, layer := range image.Layers {
		if _, err := os.Stat(layer); err != nil {
			return nil, false
		}
	}
	return &image, true
}

// saveRecord remembers what a reference resolved to. A record that
// cannot be written only costs the next run a question to the registry.
func saveRecord(path string, image *Image) {
	data, err := json.Marshal(image)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), cacheDirMode); err != nil {
		return
	}
	os.WriteFile(path, data, recordMode)
}

// pull fetches an image from its registry into the cache. A layer the
// cache already holds is not fetched again. Each layer fetched is named
// on progress.
func pull(ctx context.Context, parsed name.Reference, cacheDir string, progress io.Writer) (*Image, error) {
	platform := v1.Platform{OS: guestOS, Architecture: guestArch}
	remoteImage, err := remote.Image(parsed,
		remote.WithContext(ctx),
		remote.WithPlatform(platform),
		remote.WithAuthFromKeychain(authn.DefaultKeychain),
	)
	if err != nil {
		return nil, fmt.Errorf("image %s: %w", parsed.Name(), err)
	}

	// An image published for one platform only is returned whatever
	// platform was asked for, so the platform is checked here.
	config, err := remoteImage.ConfigFile()
	if err != nil {
		return nil, fmt.Errorf("image %s: %w", parsed.Name(), err)
	}
	if config.OS != guestOS || config.Architecture != guestArch {
		return nil, fmt.Errorf("image %s is %s/%s and has no %s/%s variant: the guest is aarch64",
			parsed.Name(), config.OS, config.Architecture, guestOS, guestArch)
	}

	layers, err := remoteImage.Layers()
	if err != nil {
		return nil, fmt.Errorf("image %s: %w", parsed.Name(), err)
	}
	directory := filepath.Join(cacheDir, layersDir)
	if err := os.MkdirAll(directory, cacheDirMode); err != nil {
		return nil, err
	}

	image := &Image{Config: config.Config}
	for _, layer := range layers {
		path, err := fetchLayer(layer, directory, progress)
		if err != nil {
			return nil, fmt.Errorf("image %s: %w", parsed.Name(), err)
		}
		image.Layers = append(image.Layers, path)
	}
	return image, nil
}

// fetchLayer returns the path of a layer's uncompressed tar file in the
// cache, downloading the layer if the cache lacks it. The file is named
// by the hash of its uncompressed content, so the name says what is in
// the file whichever image it came with.
func fetchLayer(layer v1.Layer, directory string, progress io.Writer) (string, error) {
	contentHash, err := layer.DiffID()
	if err != nil {
		return "", err
	}
	path := filepath.Join(directory, contentHash.Hex+layerSuffix)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}

	fmt.Fprintf(progress, "pulling layer %s\n", contentHash.Hex[:12])
	content, err := layer.Uncompressed()
	if err != nil {
		return "", err
	}
	defer content.Close()

	// Written under another name and renamed, so a download that was cut
	// short never passes for a layer.
	partial, err := os.CreateTemp(directory, contentHash.Hex+partialSuffix)
	if err != nil {
		return "", err
	}
	defer os.Remove(partial.Name())
	if _, err := io.Copy(partial, content); err != nil {
		partial.Close()
		return "", err
	}
	if err := partial.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(partial.Name(), path); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	return path, nil
}
