package rootfs

import (
	"archive/tar"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// member is one entry of a layer written by writeLayer.
type member struct {
	name     string
	kind     byte
	content  string
	linkname string
	mode     int64
	uid      int
}

// writeLayer writes a tar file of members and returns its path.
func writeLayer(t *testing.T, members ...member) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "layer.tar")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	writer := tar.NewWriter(file)
	for _, m := range members {
		header := &tar.Header{
			Name:     m.name,
			Typeflag: m.kind,
			Mode:     m.mode,
			Uid:      m.uid,
			Linkname: m.linkname,
			Size:     int64(len(m.content)),
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(m.content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// names returns a directory's entry names, sorted.
func names(t *testing.T, tree Tree, path string) []string {
	t.Helper()
	entries, err := tree.Dir(path)
	if err != nil {
		t.Fatalf("Dir(%q): %v", path, err)
	}
	var out []string
	for _, entry := range entries {
		out = append(out, entry.Name)
	}
	sort.Strings(out)
	return out
}

// TestLayersMerge checks that layers stack the way an image's do: an
// upper layer's file replaces a lower one's, a whiteout removes a lower
// file, an opaque directory hides what was below while keeping what its
// own layer adds, and a directory with no entry of its own exists
// because a file under it does. Two tar files are written and read
// back as one tree.
func TestLayersMerge(t *testing.T) {
	lower := writeLayer(t,
		member{name: "etc/", kind: tar.TypeDir, mode: 0o755},
		member{name: "etc/motd", kind: tar.TypeReg, content: "lower\n", mode: 0o644},
		member{name: "etc/gone", kind: tar.TypeReg, content: "x", mode: 0o644},
		member{name: "data/", kind: tar.TypeDir, mode: 0o755},
		member{name: "data/old", kind: tar.TypeReg, content: "old", mode: 0o644},
		member{name: "usr/bin/tool", kind: tar.TypeReg, content: "#!/bin/sh\n", mode: 0o755, uid: 7},
	)
	upper := writeLayer(t,
		member{name: "etc/motd", kind: tar.TypeReg, content: "upper wins\n", mode: 0o600},
		member{name: "etc/.wh.gone", kind: tar.TypeReg},
		member{name: "data/new", kind: tar.TypeReg, content: "new", mode: 0o644},
		member{name: "data/.wh..wh..opq", kind: tar.TypeReg},
	)

	tree, err := OpenLayers([]string{lower, upper})
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()

	// The upper layer's file, whole and by range.
	data, err := tree.Read("/etc/motd", 0, 100)
	if err != nil || string(data) != "upper wins\n" {
		t.Errorf("the replaced file: %q, %v", data, err)
	}
	data, err = tree.Read("/etc/motd", 6, 4)
	if err != nil || string(data) != "wins" {
		t.Errorf("a range: %q, %v", data, err)
	}
	if data, _ := tree.Read("/etc/motd", 100, 10); len(data) != 0 {
		t.Errorf("a read from past the end: %q", data)
	}
	attr, err := tree.Attr("/etc/motd")
	if err != nil || attr.Mode != 0o600 || attr.Size != 11 || attr.Type != TypeFile {
		t.Errorf("the replaced file's attributes: %+v, %v", attr, err)
	}

	// The whiteout and the opaque directory.
	if got := names(t, tree, "/etc"); len(got) != 1 || got[0] != "motd" {
		t.Errorf("/etc holds %v", got)
	}
	if _, err := tree.Attr("/etc/gone"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the whited-out file: %v", err)
	}
	if got := names(t, tree, "/data"); len(got) != 1 || got[0] != "new" {
		t.Errorf("/data holds %v", got)
	}

	// A directory nothing declared, and its file's owner and mode.
	attr, err = tree.Attr("/usr/bin")
	if err != nil || attr.Type != TypeDir {
		t.Errorf("the implied directory: %+v, %v", attr, err)
	}
	attr, err = tree.Attr("/usr/bin/tool")
	if err != nil || attr.UID != 7 || attr.Mode != 0o755 {
		t.Errorf("the tool: %+v, %v", attr, err)
	}
	if got := names(t, tree, "/"); len(got) != 3 {
		t.Errorf("the root holds %v", got)
	}
}

// TestLayersLinks checks symbolic links, which are returned and never
// followed, and hard links, which read as the file they name and share
// its inode.
func TestLayersLinks(t *testing.T) {
	layer := writeLayer(t,
		member{name: "bin/busybox", kind: tar.TypeReg, content: "ELF", mode: 0o755},
		member{name: "bin/sh", kind: tar.TypeSymlink, linkname: "busybox", mode: 0o777},
		member{name: "bin/ls", kind: tar.TypeLink, linkname: "bin/busybox"},
	)
	tree, err := OpenLayers([]string{layer})
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()

	target, err := tree.Link("/bin/sh")
	if err != nil || target != "busybox" {
		t.Errorf("the link's target: %q, %v", target, err)
	}
	attr, _ := tree.Attr("/bin/sh")
	if attr.Type != TypeSymlink {
		t.Errorf("the link is a %q", attr.Type)
	}
	if _, err := tree.Read("/bin/sh", 0, 10); err == nil {
		t.Error("a read went through a symbolic link")
	}

	// A listing carries each entry's attributes and a link's target.
	entries, err := tree.Dir("/bin")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name == "sh" && entry.Target != "busybox" {
			t.Errorf("the link's target in the listing: %q", entry.Target)
		}
		if entry.Name == "busybox" && (entry.Attr.Size != 3 || entry.Attr.Mode != 0o755) {
			t.Errorf("the file's attributes in the listing: %+v", entry.Attr)
		}
	}

	original, _ := tree.Attr("/bin/busybox")
	linked, err := tree.Attr("/bin/ls")
	if err != nil || linked.Ino != original.Ino || linked.Type != TypeFile {
		t.Errorf("the hard link: %+v, the original: %+v, %v", linked, original, err)
	}
	data, err := tree.Read("/bin/ls", 0, 10)
	if err != nil || string(data) != "ELF" {
		t.Errorf("the hard link's bytes: %q, %v", data, err)
	}
}

// TestLayersErrors checks the errors the guest is shown: a missing
// path, a read of a directory, a listing of a file.
func TestLayersErrors(t *testing.T) {
	layer := writeLayer(t,
		member{name: "dir/", kind: tar.TypeDir, mode: 0o755},
		member{name: "dir/file", kind: tar.TypeReg, content: "x", mode: 0o644},
	)
	tree, err := OpenLayers([]string{layer})
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()

	if _, err := tree.Attr("/nope"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a missing path: %v", err)
	}
	if _, err := tree.Attr("/dir/file/under"); err == nil {
		t.Error("a path through a file resolved")
	}
	if _, err := tree.Read("/dir", 0, 1); err == nil {
		t.Error("a directory was read")
	}
	if _, err := tree.Dir("/dir/file"); err == nil {
		t.Error("a file was listed")
	}
}
