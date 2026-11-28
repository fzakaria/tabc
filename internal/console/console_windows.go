package console

import "os"

// Size is not available on this system: only an engine's pty is asked
// its size this way, and engines run on Linux.
func Size(terminal *os.File) (cols, rows int, err error) {
	return 0, 0, errNoEngine
}

// Send is not available on this system.
func Send(path string, controller *os.File) error {
	return errNoEngine
}
