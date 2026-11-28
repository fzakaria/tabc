package proc

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// envHanded names, in a started process's environment, the handle of
// the file its starter handed over. Windows passes a process no
// numbered descriptors beyond stdin, stdout and stderr, so the handle is
// inherited and its number travels in the environment.
const envHanded = "TABC_HANDED"

// handedName is the name the handed file goes by in error messages.
const handedName = "handed"

// handleBase and handleBits are how a handle's number is written in
// the environment.
const (
	handleBase = 10
	handleBits = 64
)

// killedStatus is the exit status of a process ended by KillGroup or
// Stop.
const killedStatus = 1

// stillActive is the exit status Windows reports for a process that has
// not exited (STILL_ACTIVE).
const stillActive = 259

// Lead starts a command's process in a process group of its own, so
// the interrupt key of the starter's console does not reach the
// process. KeepTerminal leaves the process on the starter's console.
// LeaveTerminal gives the process a console with no window, which the
// starter's console closing does not end, and which the processes the
// process starts inherit in place of a window each.
func Lead(command *exec.Cmd, terminal Terminal) {
	flags := uint32(windows.CREATE_NEW_PROCESS_GROUP)
	if terminal == LeaveTerminal {
		flags |= windows.CREATE_NO_WINDOW
	}
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags}
}

// Hand gives the process a command starts one more open file, which
// the process finds with Handed.
func Hand(command *exec.Cmd, file *os.File) error {
	// A handle reaches a new process only if marked as inherited and
	// listed for the process.
	handle := windows.Handle(file.Fd())
	if err := windows.SetHandleInformation(handle, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
		return err
	}
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.AdditionalInheritedHandles = append(command.SysProcAttr.AdditionalInheritedHandles, syscall.Handle(handle))

	// A command with no environment of its own would be given this
	// process's, which has to be kept when one variable is added.
	if command.Env == nil {
		command.Env = os.Environ()
	}
	command.Env = append(command.Env, envHanded+"="+strconv.FormatUint(uint64(handle), handleBase))
	return nil
}

// Handed returns the file this process's starter gave it with Hand, or
// nil if the starter gave none. The variable that names the handle is
// removed, so that no process started from here takes the number for a
// handle of its own.
func Handed() *os.File {
	text := os.Getenv(envHanded)
	os.Unsetenv(envHanded)

	handle, err := strconv.ParseUint(text, handleBase, handleBits)
	if err != nil {
		return nil
	}
	return os.NewFile(uintptr(handle), handedName)
}

// Contain puts this process in a job that ends with the process: every
// process started from here on is in the job, and Windows ends them all
// when this process exits, however it exits. A browser started for a
// container never outlives the container's supervisor that way.
//
// The job lets a process leave it (BreakAway), which is how a browser
// that belongs to the user is opened.
func Contain() error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}

	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK,
		},
	}
	_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)))
	if err != nil {
		windows.CloseHandle(job)
		return err
	}

	// The job's handle stays open for as long as this process runs: the
	// handle closing is what ends the job.
	if err := windows.AssignProcessToJobObject(job, windows.CurrentProcess()); err != nil {
		windows.CloseHandle(job)
		return err
	}
	return nil
}

// BreakAway starts a command's process outside the job Contain made,
// so the process outlives this one.
func BreakAway(command *exec.Cmd) {
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.CreationFlags |= windows.CREATE_BREAKAWAY_FROM_JOB
}

// KillGroup ends the process a command started with Lead. A process
// that called Contain takes every process it started with it.
func KillGroup(pid int) error {
	return terminate(pid)
}

// Stop ends a process. Windows has no signal that asks, so the process
// gets no time to clean up; a process that called Contain still takes
// every process it started with it.
func Stop(pid int) error {
	return terminate(pid)
}

// terminate ends one process.
func terminate(pid int) error {
	process, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(process)
	return windows.TerminateProcess(process, killedStatus)
}

// Alive reports whether a process exists. A process that may not be
// asked about exists and belongs to someone else.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	defer windows.CloseHandle(process)

	// A process that exited keeps its number for as long as anyone
	// holds a handle to it, so the number alone does not say.
	var status uint32
	if err := windows.GetExitCodeProcess(process, &status); err != nil {
		return false
	}
	return status == stillActive
}

// Become runs a program in this process's place: with this process's
// input and output, and this process ends with the program's status.
// Windows cannot replace a process's program, so this process stays
// until the program ends. Become returns only on failure.
func Become(program string, arguments, environment []string) error {
	command := exec.Command(program)
	command.Args = arguments
	command.Env = environment
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr

	err := command.Run()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		return err
	}
	os.Exit(command.ProcessState.ExitCode())
	return nil
}
