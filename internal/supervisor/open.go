package supervisor

import (
	"errors"
	"os/exec"
	"runtime"
)

// The commands that open an address in the desktop's browser, by
// system.
var openers = map[string]string{
	"darwin": "open",
	"linux":  "xdg-open",
}

// OpenInBrowser asks the desktop to open an address in the user's
// browser.
func OpenInBrowser(address string) error {
	opener, known := openers[runtime.GOOS]
	if !known {
		return errors.New("no way to open a browser on this system")
	}
	return exec.Command(opener, address).Start()
}
