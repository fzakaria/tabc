package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/fzakaria/tabc/internal/bundle"
)

// startInNetwork starts the supervisor inside the container's network
// namespace. Everything the supervisor does on the container's behalf
// happens there: the connections it makes for the guest, and the
// browser too, so not even the browser is on the host's network.
//
// The configuration says which namespace in one of three ways. A path
// names a namespace the engine made (podman): the supervisor joins it.
// An entry with no path asks the runtime for a new one (Docker, whose
// hook then attaches an interface to it). No entry means the host's.
func startInNetwork(process *exec.Cmd, loaded *bundle.Bundle) error {
	path, wanted := loaded.NetworkNamespace()
	if !wanted {
		return process.Start()
	}

	// A new namespace, made as the supervisor is.
	if path == "" {
		process.SysProcAttr.Unshareflags |= syscall.CLONE_NEWNET
		err := process.Start()

		// Without the privilege to make a namespace there is only the
		// host's network to run in. That is the case when tabc is run
		// by hand on a bundle; an engine runs a runtime with the
		// privilege.
		if errors.Is(err, syscall.EPERM) && os.Geteuid() != 0 {
			process.SysProcAttr.Unshareflags &^= syscall.CLONE_NEWNET
			fmt.Fprintln(os.Stderr, "tabc: no privilege to make a network namespace; the container uses the host's network")
			return process.Start()
		}
		return err
	}

	// An existing namespace. A namespace is a property of a thread and
	// a new process starts in its creating thread's, so this thread
	// joins, starts the supervisor and is then given up: Go ends a
	// locked thread whose goroutine exits without unlocking it.
	namespace, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("the container's network namespace: %w", err)
	}
	defer namespace.Close()

	started := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		if err := unix.Setns(int(namespace.Fd()), unix.CLONE_NEWNET); err != nil {
			started <- fmt.Errorf("joining the container's network namespace: %w", err)
			return
		}
		started <- process.Start()
	}()
	return <-started
}
