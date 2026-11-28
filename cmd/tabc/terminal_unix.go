//go:build !windows

package main

import (
	"os"

	"golang.org/x/term"
)

// actOnEscapes has nothing to do here: a terminal acts on the escape
// sequences in a program's output as it is. The function returned
// undoes nothing.
func actOnEscapes() (restore func()) {
	return func() {}
}

// terminalSize returns the size, in character cells, of the terminal
// tabc's input is.
func terminalSize() (cols, rows int, err error) {
	return term.GetSize(int(os.Stdin.Fd()))
}
