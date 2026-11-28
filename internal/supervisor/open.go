package supervisor

import (
	"errors"
	"os/exec"
	"runtime"

	"github.com/fzakaria/tabc/internal/proc"
)

// The commands that open an address in the desktop's browser, by
// system: a program and the arguments that come before the address.
// Windows opens an address through the library that opens any file by
// its kind, which takes the address as it is; the shell's `start`
// would read the address's `&` as the end of a command.
var openers = map[string][]string{
	"darwin":  {"open"},
	"linux":   {"xdg-open"},
	"windows": {"rundll32", "url.dll,FileProtocolHandler"},
}

// OpenInBrowser asks the desktop to open an address in the user's
// browser.
func OpenInBrowser(address string) error {
	opener, known := openers[runtime.GOOS]
	if !known {
		return errors.New("no way to open a browser on this system")
	}
	program, arguments := opener[0], append(opener[1:len(opener):len(opener)], address)

	// The browser is the user's and outlives this process, so the
	// opener is started outside whatever this process ends with
	// (proc.Contain).
	outside := exec.Command(program, arguments...)
	proc.BreakAway(outside)
	if outside.Start() == nil {
		return nil
	}

	// A system that does not let a process start outside is asked
	// again without: a browser that ends with this process still shows
	// the page meanwhile.
	return exec.Command(program, arguments...).Start()
}
