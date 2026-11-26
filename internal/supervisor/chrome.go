package supervisor

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// unprivilegedID is the user and group Chromium runs as when the
// supervisor is the superuser: nobody. Chromium refuses to keep its
// sandbox as the superuser, and the sandbox is one of the walls this
// runtime is for, so the browser gives up the superuser, not the
// sandbox.
const unprivilegedID = 65534

// The browser's profile, one per container. A profile of its own means
// a browser process tree of its own: no two containers share a renderer
// or any storage.
//
// The profile lives in the container's state directory, unless the
// browser runs as nobody. Nobody cannot reach a state directory under a
// user's private runtime directory, which is where a rootless engine's
// is, so then the profile is a directory of its own under sharedTemp,
// which anyone can walk into, and the pattern names it.
const (
	profileDirName = "profile"
	sharedTemp     = "/tmp"
	profilePattern = "tabc-profile-"
)

// profileMode lets only the browser's user into the profile.
const profileMode = 0o700

// chromiumFlags are the same for every container: no window, nothing
// on first run, and none of the background work a desktop browser does
// that a container has no use for.
var chromiumFlags = []string{
	"--headless=new",
	"--no-first-run",
	"--no-default-browser-check",
	"--disable-background-networking",
	"--disable-component-update",
	"--disable-default-apps",
	"--disable-extensions",
	"--disable-sync",
	"--disable-gpu",
	"--disable-dev-shm-usage",
	"--metrics-recording-only",
	"--mute-audio",
	// A page nobody looks at would otherwise have its timers slowed,
	// and the guest's clock and scheduler run on them.
	"--disable-background-timer-throttling",
	"--disable-renderer-backgrounding",
	"--disable-backgrounding-occluded-windows",
}

// makeProfile makes the browser's profile directory for a container
// whose state is in dir, owned by the user the browser will run as.
func makeProfile(dir string) (string, error) {
	if os.Geteuid() != 0 {
		profile := filepath.Join(dir, profileDirName)
		return profile, os.Mkdir(profile, profileMode)
	}

	profile, err := os.MkdirTemp(sharedTemp, profilePattern)
	if err != nil {
		return "", err
	}
	if err := os.Chown(profile, unprivilegedID, unprivilegedID); err != nil {
		os.RemoveAll(profile)
		return "", fmt.Errorf("handing the profile to the browser's user: %w", err)
	}
	return profile, nil
}

// startBrowser launches Chromium on the page at url, with its profile
// in profile and its own output in log. extraFlags come after tabc's
// own, so they can override them.
func startBrowser(binary, profile, url string, extraFlags []string, log *os.File) (*exec.Cmd, error) {
	if binary == "" {
		return nil, errors.New("no Chromium or Chrome found; set TABC_CHROMIUM, or use --open to run in your own browser")
	}
	args := append([]string{}, chromiumFlags...)
	args = append(args, "--user-data-dir="+profile)
	args = append(args, extraFlags...)
	args = append(args, url)

	browser := exec.Command(binary, args...)
	browser.Stdout = log
	browser.Stderr = log
	browser.Env = append(os.Environ(),
		"HOME="+profile,
		"XDG_CONFIG_HOME="+filepath.Join(profile, "config"),
		"XDG_CACHE_HOME="+filepath.Join(profile, "cache"),
	)

	// As the superuser, run the browser as nobody.
	if os.Geteuid() == 0 {
		browser.SysProcAttr = &syscall.SysProcAttr{
			Credential: &syscall.Credential{Uid: unprivilegedID, Gid: unprivilegedID},
		}
	}

	if err := browser.Start(); err != nil {
		return nil, fmt.Errorf("starting %s: %w", binary, err)
	}
	return browser, nil
}
