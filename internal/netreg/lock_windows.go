package netreg

import (
	"os"

	"golang.org/x/sys/windows"
)

// Windows locks a range of a file's bytes, not the file. The lock file
// is empty, and a lock may cover bytes a file does not have, so every
// process locks the same one byte.
const (
	lockOffset = 0
	lockLength = 1
)

// lockFile waits until no other process holds the lock on file, and
// takes the lock.
func lockFile(file *os.File) error {
	return windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, lockLength, 0, &windows.Overlapped{Offset: lockOffset})
}

// unlockFile gives back the lock lockFile took.
func unlockFile(file *os.File) {
	windows.UnlockFileEx(windows.Handle(file.Fd()), 0, lockLength, 0, &windows.Overlapped{Offset: lockOffset})
}
