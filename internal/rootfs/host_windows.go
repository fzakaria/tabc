package rootfs

import (
	"errors"
	"hash/fnv"
	"io/fs"
	"os"
	"path"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// What a DirTree asks of the host, on Windows.
//
// A Windows file system has no owner numbers and no permission bits,
// so a volume's files read the way a Windows directory mounted into a
// Linux container reads under Docker: owned by root, with a mode made
// up from the file's kind, and a change of mode or owner is accepted
// and changes nothing.

// noFollow is nothing on Windows, which has no flag for opening a name
// only if the name is not a symbolic link. os.Root follows a link only
// to a file inside the tree, so a file replaced by a link under the
// guest still names nothing outside the volume.
const noFollow = 0

// The modes a volume's files are shown with. Windows has no execute
// bit, and a program or a script kept on a volume has to run, so every
// file is executable. A file marked read-only has no write bits.
const (
	modeWritable = 0o755
	modeReadOnly = 0o555
	modeSymlink  = 0o777
)

// ownerWrite is the bit of a mode that lets a file's owner write to it.
const ownerWrite = 0o200

// singleLink is the link count of a file whose count cannot be asked.
const singleLink = 1

// leafSeparators are the characters a name inside one directory cannot
// have: the two separators, and the colon that names a file's stream.
const leafSeparators = `\/:`

// indexHighShift is where the high half of a file's index goes in the
// 64-bit number the two halves make.
const indexHighShift = 32

// statAttr fills in what a Windows stat result can say of mode, owner
// and link count. The inode number and the true link count take a
// handle to the file, which identify opens.
func statAttr(attr *Attr, info fs.FileInfo) {
	attr.Nlink = singleLink
	switch {
	case attr.Type == TypeSymlink:
		attr.Mode = modeSymlink
	case info.Mode().Perm()&ownerWrite == 0:
		attr.Mode = modeReadOnly
	default:
		attr.Mode = modeWritable
	}
}

// identify fills in the inode number and the link count of the file at
// name, a path relative to the tree's root. parent is the file's
// directory if the caller has the directory open, and nil otherwise.
//
// Windows reports both only for an open file. A file that cannot be
// opened keeps a link count of one and gets a number made from its
// path, so the guest still sees a number that is the file's own.
func (t *DirTree) identify(attr *Attr, name string, parent *os.File) {
	attr.Ino = pathNumber(name)

	// The root has no directory above it inside the tree, and is asked
	// through its own handle.
	if name == rootName {
		directory, err := t.root.Open(rootName)
		if err != nil {
			return
		}
		defer directory.Close()
		withHandle(directory, func(handle windows.Handle) {
			identifyHandle(attr, handle)
		})
		return
	}

	// Any other file is opened by its bare name from its directory,
	// which os.Root opened and so is inside the tree.
	if parent == nil {
		opened, err := t.root.Open(path.Dir(name))
		if err != nil {
			return
		}
		defer opened.Close()
		parent = opened
	}
	withHandle(parent, func(directory windows.Handle) {
		handle, err := openLeaf(directory, path.Base(name))
		if err != nil {
			return
		}
		defer windows.CloseHandle(handle)
		identifyHandle(attr, handle)
	})
}

// identifyHandle fills in the inode number and the link count of an
// open file.
func identifyHandle(attr *Attr, handle windows.Handle) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return
	}
	attr.Ino = uint64(info.FileIndexHigh)<<indexHighShift | uint64(info.FileIndexLow)
	attr.Nlink = uint64(info.NumberOfLinks)
}

// withHandle calls use with a file's handle, which stays valid for the
// length of the call.
func withHandle(file *os.File, use func(windows.Handle)) {
	connection, err := file.SyscallConn()
	if err != nil {
		return
	}
	connection.Control(func(handle uintptr) {
		use(windows.Handle(handle))
	})
}

// openLeaf opens the file called leaf in an open directory, to ask the
// file's attributes and nothing else. A symbolic link is opened as the
// link, not as what the link points to, so nothing outside the
// directory is reached.
func openLeaf(directory windows.Handle, leaf string) (windows.Handle, error) {
	// One name, in this directory: a separator would walk on from here,
	// through whatever links are on the way.
	if leaf == "" || leaf == ".." || strings.ContainsAny(leaf, leafSeparators) {
		return windows.InvalidHandle, fs.ErrInvalid
	}
	name, err := windows.NewNTUnicodeString(leaf)
	if err != nil {
		return windows.InvalidHandle, err
	}

	object := windows.OBJECT_ATTRIBUTES{
		RootDirectory: directory,
		ObjectName:    name,
		Attributes:    windows.OBJ_CASE_INSENSITIVE,
	}
	object.Length = uint32(unsafe.Sizeof(object))

	// Asking for attributes only, and sharing everything, opens a file
	// another program holds open for itself alone.
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(
		&handle,
		windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE,
		&object,
		&status,
		nil,
		0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN,
		windows.FILE_OPEN_REPARSE_POINT|windows.FILE_OPEN_FOR_BACKUP_INTENT|windows.FILE_SYNCHRONOUS_IO_NONALERT,
		0,
		0,
	)
	if err != nil {
		return windows.InvalidHandle, err
	}
	return handle, nil
}

// pathNumber returns a number for a path, the same for the same path
// and in all likelihood different for different ones.
func pathNumber(name string) uint64 {
	hash := fnv.New64a()
	hash.Write([]byte(name))
	return hash.Sum64()
}

// createMode is the mode a new file is made with. Windows makes a file
// without the owner's write bit read-only, and the guest writes to the
// file it just made whatever mode it asked for, so the bit is always
// set.
func createMode(mode uint32) os.FileMode {
	return os.FileMode(mode&permissionBits) | ownerWrite
}

// chmod accepts a change of permissions and changes nothing: a
// Windows file has no permission bits to keep them in.
func (t *DirTree) chmod(string, uint32) error {
	return nil
}

// chown accepts a change of owner and changes nothing: a Windows file
// has no owner numbers.
func (t *DirTree) chown(string, int, int) error {
	return nil
}

// hostErrors are the Windows errors that Go leaves without the errno
// failWith answers the guest by, each with that errno.
var hostErrors = []struct {
	host  syscall.Errno
	errno syscall.Errno
}{
	{windows.ERROR_DIR_NOT_EMPTY, syscall.ENOTEMPTY},
	{windows.ERROR_NOT_SAME_DEVICE, syscall.EXDEV},
	{windows.ERROR_TOO_MANY_LINKS, syscall.EMLINK},
	{windows.ERROR_INVALID_NAME, syscall.EINVAL},
	// Making a symbolic link takes a privilege that only an
	// administrator, or anyone with Developer Mode on, holds.
	{windows.ERROR_PRIVILEGE_NOT_HELD, syscall.EPERM},
}

// hostError returns a file system error as failWith reads errors: a
// Windows error is replaced by the errno that says the same.
func hostError(err error) error {
	for _, known := range hostErrors {
		if errors.Is(err, known.host) {
			return known.errno
		}
	}
	return err
}
