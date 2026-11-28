//go:build !windows

package state

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// defaultRoot is the root for the superuser.
const (
	systemRunDir = "/run"
	defaultRoot  = systemRunDir + "/" + rootDirName
)

// accessWrite asks access(2) whether a path may be written (W_OK).
const accessWrite = 2

// ownRoot returns the directory state is kept under when none is
// given: /run/tabc for the superuser and a directory under
// XDG_RUNTIME_DIR, or the temporary directory, for anyone else.
//
// A rootless engine runs the runtime as the superuser of a user
// namespace, who cannot write to /run. That superuser is anyone else:
// what decides is whether /run can be written, not the user's number.
func ownRoot() string {
	if os.Geteuid() == 0 && syscall.Access(systemRunDir, accessWrite) == nil {
		return defaultRoot
	}
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		return filepath.Join(runtimeDir, rootDirName)
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("%s-%d", rootDirName, os.Geteuid()))
}
