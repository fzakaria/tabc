package state

import (
	"os"
	"path/filepath"
)

// ownRoot returns the directory state is kept under when none is
// given: a directory under the user's temporary directory, which
// Windows keeps apart for each user.
func ownRoot() string {
	return filepath.Join(os.TempDir(), rootDirName)
}
