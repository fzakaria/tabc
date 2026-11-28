//go:build !windows

package proc

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// handedDescriptor is where a process finds the file its starter handed
// over: the first descriptor after stdin, stdout and stderr.
const handedDescriptor = 3

// handedName is the name the handed file goes by in error messages.
const handedName = "handed"

// probeSignal is the signal that checks for a process and sends
// nothing.
const probeSignal = syscall.Signal(0)

// Lead makes the process a command starts the leader of a session of
// its own, which keeps the process apart from the starter's terminal
// and process group whichever Terminal is asked for: a session has no
// terminal until its leader opens one.
func Lead(command *exec.Cmd, _ Terminal) {
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// Hand gives the process a command starts one more open file, which
// the process finds with Handed.
func Hand(command *exec.Cmd, file *os.File) error {
	command.ExtraFiles = []*os.File{file}
	return nil
}

// Handed returns the file this process's starter gave it with Hand.
func Handed() *os.File {
	return os.NewFile(handedDescriptor, handedName)
}

// Contain has nothing to do here: Lead already made this process the
// leader of a session, and KillGroup ends the session's process group.
func Contain() error {
	return nil
}

// KillGroup ends the process a command started with Lead, and every
// process in that process's group.
func KillGroup(pid int) error {
	return syscall.Kill(-pid, syscall.SIGKILL)
}

// Stop asks a process to end, which leaves the process time to clean
// up.
func Stop(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}

// Alive reports whether a process exists. EPERM means the process
// exists and belongs to someone else.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, probeSignal)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// Become replaces this process with a program. Become returns only on
// failure.
func Become(program string, arguments, environment []string) error {
	return syscall.Exec(program, arguments, environment)
}

// BreakAway has nothing to do here: a process started from a session's
// leader outlives the leader unless the session's group is signalled.
func BreakAway(*exec.Cmd) {}
