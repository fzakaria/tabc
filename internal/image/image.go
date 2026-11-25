// Package image fetches a container image from a registry for `tabc
// run`, which has no engine to do that for it. The layers are kept as
// uncompressed tar files in a cache and never unpacked: the root file
// system is read out of them in place (internal/rootfs, LayerTree).
package image

import (
	"context"
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

// Where the cache keeps layers under its root, a layer's file name, and
// the name a layer has while it downloads.
const (
	layersDir     = "layers"
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

// Pull fetches the image a reference names, such as "alpine" or
// "ghcr.io/owner/name:tag", into the cache under cacheDir. A layer the
// cache already holds is not fetched again. Each layer fetched is named
// on progress.
func Pull(ctx context.Context, reference, cacheDir string, progress io.Writer) (*Image, error) {
	parsed, err := name.ParseReference(reference)
	if err != nil {
		return nil, fmt.Errorf("image %q: %w", reference, err)
	}

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
