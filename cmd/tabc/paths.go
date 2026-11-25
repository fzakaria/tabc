package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"

	"github.com/fzakaria/tabc/internal/assets"
)

// chromiumBuilt is the browser the package that built tabc pins, set by
// the linker (nix/package.nix) and empty in a plain `go build`.
var chromiumBuilt string

// The variables that override the files tabc serves a page and the
// browser tabc runs.
const (
	envShare    = "TABC_SHARE"
	envChromium = "TABC_CHROMIUM"
)

// browserNames are the names Chromium goes by on a PATH, and
// browserPaths where a Mac keeps the application, which is on no PATH.
var (
	browserNames = []string{"chromium", "chromium-browser", "google-chrome-stable", "google-chrome"}
	browserPaths = []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
	}
)

// share returns the page, the SDK and the guest's init: the directory
// the environment names, else the files built into the binary.
func share() (fs.FS, error) {
	if dir := os.Getenv(envShare); dir != "" {
		return os.DirFS(dir), nil
	}
	if files := assets.Embedded(); files != nil {
		return files, nil
	}
	return nil, fmt.Errorf("this tabc was built without its page and kernel; set %s to the output of `nix build .#share`", envShare)
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
	for _, path := range browserPaths {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", errors.New("no Chromium or Chrome found; set " + envChromium)
}
