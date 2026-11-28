package main

import (
	"os"

	"golang.org/x/sys/windows"
	"golang.org/x/term"
)

// actOnEscapes makes the console tabc's output goes to act on the
// escape sequences a program writes: colours, the cursor's moves, the
// screen an editor draws. An older Windows console prints them as
// text unless asked. The function returned puts the console back as it
// was.
//
// The mode is the console's, not this process's, so the supervisor,
// which is who writes the program's output, is covered too.
func actOnEscapes() (restore func()) {
	console := windows.Handle(os.Stdout.Fd())

	// Output that is not a console has no mode, and nothing to change.
	var before uint32
	if err := windows.GetConsoleMode(console, &before); err != nil {
		return func() {}
	}

	wanted := before | windows.ENABLE_PROCESSED_OUTPUT | windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING
	if err := windows.SetConsoleMode(console, wanted); err != nil {
		return func() {}
	}
	return func() { windows.SetConsoleMode(console, before) }
}

// terminalSize returns the size, in character cells, of the console
// tabc stands on. Windows tells a console's size by its output, not by
// its input.
func terminalSize() (cols, rows int, err error) {
	return term.GetSize(int(os.Stdout.Fd()))
}
