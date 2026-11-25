//go:build !linux

package console

import (
	"errors"
	"os"
)

// errNoEngine is why there is no pty to make: a console socket is how
// a container engine asks for a terminal, and engines run on Linux.
// `tabc run -t` uses the terminal tabc itself was started on.
var errNoEngine = errors.New("a console socket is only supported on Linux")

// Open is not available on this system.
func Open() (controller, terminal *os.File, err error) {
	return nil, nil, errNoEngine
}

// MakeRaw is not available on this system.
func MakeRaw(terminal *os.File) error {
	return errNoEngine
}
