package rootfs

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
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

// rootName is the root's own path as os.Root takes paths.
const rootName = "."

// relative turns the absolute path the guest uses into the path os.Root
// takes, which is relative to the root: "/" is rootName.
func relative(path string) string {
	path = strings.Trim(path, "/")
	if path == "" {
		return rootName
	}
	return path
}

// Attr returns a path's attributes. A file of a kind the guest cannot
// be shown reads as not there.
func (t *DirTree) Attr(path string) (Attr, error) {
	name := relative(path)
	info, err := t.root.Lstat(name)
	if err != nil {
		return Attr{}, err
	}
	attr, known := attrOf(info)
	if !known {
		return Attr{}, fs.ErrNotExist
	}
	t.identify(&attr, name, nil)
	return attr, nil
}

// Dir returns a directory's entries.
func (t *DirTree) Dir(name string) ([]Entry, error) {
	directory, err := t.root.Open(relative(name))
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
		itemName := path.Join(relative(name), item.Name())
		t.identify(&attr, itemName, directory)

		entry := Entry{Name: item.Name(), Type: attr.Type, Ino: attr.Ino, Attr: attr}
		if attr.Type == TypeSymlink {
			entry.Target, _ = t.Link(itemName)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// Link returns a symbolic link's target, with the guest's separator: a
// link read on Windows has Windows's, and ToSlash changes nothing
// anywhere else.
func (t *DirTree) Link(path string) (string, error) {
	target, err := t.root.Readlink(relative(path))
	return filepath.ToSlash(target), err
}

// Read returns a range of a regular file's bytes.
func (t *DirTree) Read(path string, offset, length int64) ([]byte, error) {
	// O_NOFOLLOW: the guest opens only what its own lookup found to be a
	// regular file, so a link here is a file replaced under the guest.
	file, err := t.root.OpenFile(relative(path), os.O_RDONLY|noFollow, 0)
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

	// The mode, owner, link count and inode number are the host's to
	// say, each host in its own way.
	statAttr(&attr, info)
	return attr, true
}
