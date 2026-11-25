package rootfs

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"syscall"
)

// LayerTree is a root file system read straight out of an image's
// layers. A layer is a tar file of what changed since the layer below.
// Nothing is unpacked: opening the tree reads each layer's headers
// once, remembers where every file's bytes are, and a read is a read of
// that range of the tar file.
//
// Unpacking would need a file system that can hold what a Linux image
// holds: other users' files, set-user-id bits, names that differ only
// in case. A Mac's cannot without help, and here the host's file system
// never has to.

// The names a layer uses to say that something below is gone: a file
// named whiteoutPrefix+name removes name, and a file named opaqueMarker
// in a directory hides everything the layers below put there.
const (
	whiteoutPrefix = ".wh."
	opaqueMarker   = ".wh..wh..opq"
)

// What a directory the layers imply and never declare looks like, and
// the link count and size every directory is given.
const (
	impliedDirMode = 0o755
	dirNlink       = 2
	dirSize        = 4096
	linkMode       = 0o777
)

// rootIno is the root directory's inode number; the others count up
// from it.
const rootIno = 1

// node is one path in the merged tree.
type node struct {
	attr Attr

	// layer is the index of the layer that last put this node here,
	// which is what a whiteout in a later layer may remove.
	layer int

	// A directory's entries.
	children map[string]*node

	// A symbolic link's target.
	target string

	// A regular file's bytes: the layer's tar file, and where in it.
	file   *os.File
	offset int64
}

// LayerTree is the merged tree of an image's layers.
type LayerTree struct {
	root    *node
	files   []*os.File
	nextIno uint64
}

// OpenLayers returns the tree of the layers at paths, uncompressed tar
// files, lowest first.
func OpenLayers(paths []string) (*LayerTree, error) {
	tree := &LayerTree{nextIno: rootIno}
	tree.root = tree.newDir(0)

	for index, layerPath := range paths {
		file, err := os.Open(layerPath)
		if err != nil {
			tree.Close()
			return nil, err
		}
		tree.files = append(tree.files, file)

		if err := tree.apply(file, index); err != nil {
			tree.Close()
			return nil, fmt.Errorf("layer %s: %w", layerPath, err)
		}
	}
	return tree, nil
}

// Close closes the layers' files.
func (t *LayerTree) Close() error {
	for _, file := range t.files {
		file.Close()
	}
	return nil
}

// ino returns a fresh inode number.
func (t *LayerTree) ino() uint64 {
	number := t.nextIno
	t.nextIno++
	return number
}

// newDir returns an empty directory as layer `layer` implies one.
func (t *LayerTree) newDir(layer int) *node {
	return &node{
		attr:     Attr{Type: TypeDir, Mode: impliedDirMode, Nlink: dirNlink, Size: dirSize, Ino: t.ino()},
		layer:    layer,
		children: map[string]*node{},
	}
}

// apply merges one layer into the tree.
func (t *LayerTree) apply(file *os.File, layer int) error {
	reader := tar.NewReader(file)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}

		// The reader has consumed exactly the header, so the file's own
		// position is where this member's bytes begin.
		offset, err := file.Seek(0, io.SeekCurrent)
		if err != nil {
			return err
		}

		name := path.Clean("/" + header.Name)
		if name == "/" {
			t.root.attr = dirAttr(header, t.root.attr.Ino)
			continue
		}
		parent := t.mkdirAll(path.Dir(name), layer)
		base := path.Base(name)

		// What this layer says is gone from the layers below. Only
		// lower layers' entries go: the marker may come after entries
		// its own layer adds to the same directory.
		if base == opaqueMarker {
			for child, existing := range parent.children {
				if existing.layer < layer {
					delete(parent.children, child)
				}
			}
			continue
		}
		if hidden, isWhiteout := strings.CutPrefix(base, whiteoutPrefix); isWhiteout {
			if existing, has := parent.children[hidden]; has && existing.layer < layer {
				delete(parent.children, hidden)
			}
			continue
		}

		switch header.Typeflag {
		case tar.TypeDir:
			// A directory that is already there keeps its entries and
			// takes the newer attributes.
			existing, has := parent.children[base]
			if has && existing.attr.Type == TypeDir {
				existing.attr = dirAttr(header, existing.attr.Ino)
				existing.layer = layer
				continue
			}
			directory := t.newDir(layer)
			directory.attr = dirAttr(header, directory.attr.Ino)
			parent.children[base] = directory

		case tar.TypeReg:
			parent.children[base] = &node{
				attr: Attr{
					Type:    TypeFile,
					Mode:    uint32(header.Mode) & permissionBits,
					Size:    header.Size,
					UID:     uint32(header.Uid),
					GID:     uint32(header.Gid),
					Nlink:   1,
					MtimeMs: header.ModTime.UnixMilli(),
					Ino:     t.ino(),
				},
				layer:  layer,
				file:   file,
				offset: offset,
			}

		case tar.TypeSymlink:
			parent.children[base] = &node{
				attr: Attr{
					Type:    TypeSymlink,
					Mode:    linkMode,
					Size:    int64(len(header.Linkname)),
					UID:     uint32(header.Uid),
					GID:     uint32(header.Gid),
					Nlink:   1,
					MtimeMs: header.ModTime.UnixMilli(),
					Ino:     t.ino(),
				},
				layer:  layer,
				target: header.Linkname,
			}

		case tar.TypeLink:
			// A hard link is the file it names, under another name: the
			// same bytes and the same inode.
			original, err := t.lookup(path.Clean("/" + header.Linkname))
			if err != nil || original.attr.Type != TypeFile {
				continue
			}
			linked := *original
			linked.layer = layer
			parent.children[base] = &linked
		}
	}
}

// dirAttr returns a directory's attributes from its header, keeping the
// inode number the directory already has.
func dirAttr(header *tar.Header, ino uint64) Attr {
	return Attr{
		Type:    TypeDir,
		Mode:    uint32(header.Mode) & permissionBits,
		Size:    dirSize,
		UID:     uint32(header.Uid),
		GID:     uint32(header.Gid),
		Nlink:   dirNlink,
		MtimeMs: header.ModTime.UnixMilli(),
		Ino:     ino,
	}
}

// mkdirAll returns the directory at name, making the directories on the
// way that no layer has declared. Something else in the way, a file
// where a later layer puts a directory, is replaced.
func (t *LayerTree) mkdirAll(name string, layer int) *node {
	current := t.root
	for _, part := range strings.Split(strings.Trim(name, "/"), "/") {
		if part == "" {
			continue
		}
		next, has := current.children[part]
		if !has || next.attr.Type != TypeDir {
			next = t.newDir(layer)
			current.children[part] = next
		}
		current = next
	}
	return current
}

// lookup returns the node at name, without following links.
func (t *LayerTree) lookup(name string) (*node, error) {
	current := t.root
	for _, part := range strings.Split(strings.Trim(name, "/"), "/") {
		if part == "" {
			continue
		}
		if current.attr.Type != TypeDir {
			return nil, syscall.ENOTDIR
		}
		next, has := current.children[part]
		if !has {
			return nil, fs.ErrNotExist
		}
		current = next
	}
	return current, nil
}

// Attr returns a path's attributes.
func (t *LayerTree) Attr(name string) (Attr, error) {
	found, err := t.lookup(name)
	if err != nil {
		return Attr{}, err
	}
	return found.attr, nil
}

// Dir returns a directory's entries.
func (t *LayerTree) Dir(name string) ([]Entry, error) {
	found, err := t.lookup(name)
	if err != nil {
		return nil, err
	}
	if found.attr.Type != TypeDir {
		return nil, syscall.ENOTDIR
	}

	entries := make([]Entry, 0, len(found.children))
	for child, item := range found.children {
		entries = append(entries, Entry{Name: child, Type: item.attr.Type, Ino: item.attr.Ino})
	}
	return entries, nil
}

// Link returns a symbolic link's target.
func (t *LayerTree) Link(name string) (string, error) {
	found, err := t.lookup(name)
	if err != nil {
		return "", err
	}
	if found.attr.Type != TypeSymlink {
		return "", fs.ErrInvalid
	}
	return found.target, nil
}

// Read returns a range of a regular file's bytes, from the layer the
// file is in.
func (t *LayerTree) Read(name string, offset, length int64) ([]byte, error) {
	found, err := t.lookup(name)
	if err != nil {
		return nil, err
	}
	if found.attr.Type == TypeDir {
		return nil, syscall.EISDIR
	}
	if found.attr.Type != TypeFile {
		return nil, fs.ErrInvalid
	}

	// The file ends where its member does, not where the tar file does.
	if offset >= found.attr.Size {
		return nil, nil
	}
	length = min(length, found.attr.Size-offset)

	buffer := make([]byte, length)
	count, err := found.file.ReadAt(buffer, found.offset+offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buffer[:count], nil
}
