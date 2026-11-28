//go:build !windows

package rootfs

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

// What a DirTree asks of the host that Linux and macOS answer one way
// and Windows another (host_windows.go).

// noFollow opens a file only if its name is not a symbolic link.
const noFollow = syscall.O_NOFOLLOW

// statAttr fills in what only the raw stat result carries: the mode's
// permission and special bits, the owner, the link count and the inode
// number. The fields' widths differ between systems, hence the
// conversions.
func statAttr(attr *Attr, info fs.FileInfo) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return
	}
	attr.Mode = uint32(stat.Mode) & permissionBits
	attr.UID = stat.Uid
	attr.GID = stat.Gid
	attr.Nlink = uint64(stat.Nlink)
	attr.Ino = uint64(stat.Ino)
}

// identify has nothing to add here: statAttr already found the inode
// number and the link count in the stat result.
func (t *DirTree) identify(*Attr, string, *os.File) {}

// createMode is the mode a new file is made with: the one asked for.
func createMode(mode uint32) os.FileMode {
	return os.FileMode(mode & permissionBits)
}

// chmod sets a path's permissions.
func (t *DirTree) chmod(name string, mode uint32) error {
	return t.root.Chmod(name, os.FileMode(mode&permissionBits))
}

// chown sets a path's owner; -1 leaves the user or the group alone. A
// change that the supervisor's user may not make is let go: a
// container's root is not the host's, and a volume's files stay
// whoever's they are.
func (t *DirTree) chown(name string, uid, gid int) error {
	err := t.root.Lchown(name, uid, gid)
	if errors.Is(err, os.ErrPermission) {
		return nil
	}
	return err
}

// hostError returns a file system error as failWith reads errors: here
// the host's errors are already the errno values failWith knows.
func hostError(err error) error {
	return err
}
