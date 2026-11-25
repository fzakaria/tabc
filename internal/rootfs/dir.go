package rootfs

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
	"syscall"
)

// DirTree is a root file system in a directory on the host: what a
// container engine unpacks an image into and hands the runtime.
// Requests go through os.Root, which refuses any path that would leave
// the directory, a link pointing outside it included, so a page that
// was taken over still cannot read the host's files.
type DirTree struct {
	root *os.Root
}

// OpenDir returns the tree for the directory at path.
func OpenDir(path string) (*DirTree, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	return &DirTree{root: root}, nil
}

// Close releases the directory.
func (t *DirTree) Close() error {
	return t.root.Close()
}

// relative turns the absolute path the guest uses into the path os.Root
// takes, which is relative to the root: "/" is ".".
func relative(path string) string {
	path = strings.Trim(path, "/")
	if path == "" {
		return "."
	}
	return path
}

// Attr returns a path's attributes. A file of a kind the guest cannot
// be shown reads as not there.
func (t *DirTree) Attr(path string) (Attr, error) {
	info, err := t.root.Lstat(relative(path))
	if err != nil {
		return Attr{}, err
	}
	attr, known := attrOf(info)
	if !known {
		return Attr{}, fs.ErrNotExist
	}
	return attr, nil
}

// Dir returns a directory's entries.
func (t *DirTree) Dir(path string) ([]Entry, error) {
	directory, err := t.root.Open(relative(path))
	if err != nil {
		return nil, err
	}
	defer directory.Close()

	listed, err := directory.ReadDir(-1)
	if err != nil {
		return nil, err
	}

	// Each entry needs its inode number, which only its own attributes
	// carry. An entry that vanished meanwhile, or is of a kind the guest
	// cannot be shown, is left out.
	entries := make([]Entry, 0, len(listed))
	for _, item := range listed {
		info, err := item.Info()
		if err != nil {
			continue
		}
		attr, known := attrOf(info)
		if !known {
			continue
		}
		entries = append(entries, Entry{Name: item.Name(), Type: attr.Type, Ino: attr.Ino})
	}
	return entries, nil
}

// Link returns a symbolic link's target.
func (t *DirTree) Link(path string) (string, error) {
	return t.root.Readlink(relative(path))
}

// Read returns a range of a regular file's bytes.
func (t *DirTree) Read(path string, offset, length int64) ([]byte, error) {
	// O_NOFOLLOW: the guest opens only what its own lookup found to be a
	// regular file, so a link here is a file replaced under the guest.
	file, err := t.root.OpenFile(relative(path), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	buffer := make([]byte, length)
	count, err := file.ReadAt(buffer, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buffer[:count], nil
}

// attrOf returns the attributes of a file from its lstat result, and
// false for a kind of file the guest cannot be shown.
func attrOf(info fs.FileInfo) (Attr, bool) {
	var kind string
	switch {
	case info.Mode().IsRegular():
		kind = TypeFile
	case info.IsDir():
		kind = TypeDir
	case info.Mode()&fs.ModeSymlink != 0:
		kind = TypeSymlink
	default:
		return Attr{}, false
	}

	attr := Attr{
		Type:    kind,
		Size:    info.Size(),
		MtimeMs: info.ModTime().UnixMilli(),
	}

	// The owner, link count, inode and the mode's special bits are in
	// the raw stat result only. The fields' widths differ between
	// systems, hence the conversions.
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		attr.Mode = uint32(stat.Mode) & permissionBits
		attr.UID = stat.Uid
		attr.GID = stat.Gid
		attr.Nlink = uint64(stat.Nlink)
		attr.Ino = uint64(stat.Ino)
	}
	return attr, true
}
