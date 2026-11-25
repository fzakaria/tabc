package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// shareDirBuilt and chromiumBuilt are where the package that built
// tabc put the page, the SDK and the guest's init, and the browser the
// package pins. Both are set by the linker (nix/package.nix) and empty
// in a plain `go build`.
var (
	shareDirBuilt string
	chromiumBuilt string
)

// The variables that override both.
const (
	envShare    = "TABC_SHARE"
	envChromium = "TABC_CHROMIUM"
)

// relativeShare is the share directory next to an installed binary.
const relativeShare = "../share/tabc"

// browserNames are the names Chromium goes by on a PATH.
var browserNames = []string{"chromium", "chromium-browser", "google-chrome-stable", "google-chrome"}

// shareDir returns the directory holding the page, the SDK and the
// guest's init: the environment's, the built-in one, or the one next to
// the binary.
func shareDir() (string, error) {
	if dir := os.Getenv(envShare); dir != "" {
		return dir, nil
	}
	if shareDirBuilt != "" {
		return shareDirBuilt, nil
	}

	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(filepath.Dir(self), relativeShare)
	if _, err := os.Stat(dir); err != nil {
		return "", fmt.Errorf("no share directory at %s; set %s", dir, envShare)
	}
	return dir, nil
}

// chromiumPath returns the browser to run: the environment's, the
// built-in one, or the first on the PATH.
func chromiumPath() (string, error) {
	if path := os.Getenv(envChromium); path != "" {
		return path, nil
	}
	if chromiumBuilt != "" {
		return chromiumBuilt, nil
	}
	for _, name := range browserNames {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", errors.New("no Chromium found; set " + envChromium)
}
