package supervisor

import (
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

// profileDirName is the browser's profile, one per container, under
// the container's state directory. A profile of its own means a
// browser process tree of its own: no two containers share a renderer
// or any storage.
const profileDirName = "profile"

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

// startBrowser launches Chromium on the page at url, with its profile
// under dir and its own output in log. extraFlags come after tabc's
// own, so they can override them.
func startBrowser(binary, dir, url string, extraFlags []string, log *os.File) (*exec.Cmd, error) {
	profile := filepath.Join(dir, profileDirName)
	if err := os.Mkdir(profile, profileMode); err != nil {
		return nil, err
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

	// As the superuser, hand the profile to nobody and run the browser
	// as nobody.
	if os.Geteuid() == 0 {
		if err := os.Chown(profile, unprivilegedID, unprivilegedID); err != nil {
			return nil, fmt.Errorf("handing the profile to the browser's user: %w", err)
		}
		browser.SysProcAttr = &syscall.SysProcAttr{
			Credential: &syscall.Credential{Uid: unprivilegedID, Gid: unprivilegedID},
		}
	}

	if err := browser.Start(); err != nil {
		return nil, fmt.Errorf("starting %s: %w", binary, err)
	}
	return browser, nil
}
