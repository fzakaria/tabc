//go:build !windows

package netreg

import (
	"os"
	"syscall"
)

// lockFile waits until no other process holds the lock on file, and
// takes the lock.
func lockFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
}

// unlockFile gives back the lock lockFile took.
func unlockFile(file *os.File) {
	syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
