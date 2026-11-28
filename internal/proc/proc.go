// Package proc is what tabc asks of the host about processes: starting
// one that leads a group of its own, handing the new process a file,
// ending a group, and asking whether a process still exists.
//
// A container is a supervisor and the browser the supervisor started.
// Linux and macOS keep the two together as a session, which a signal to
// the session's process group ends. Windows has neither sessions nor
// signals, and keeps the two together as a job that ends when the
// supervisor does.
package proc

// Terminal says whether a process that leads a group of its own keeps
// the terminal its starter stands on.
type Terminal int

const (
	// KeepTerminal is for a process whose input and output are its
	// starter's: a container's supervisor, which reads the keys typed at
	// `tabc run -it`.
	KeepTerminal Terminal = iota
	// LeaveTerminal is for a process that outlives its starter's
	// terminal: the daemon tocker starts.
	LeaveTerminal
)
