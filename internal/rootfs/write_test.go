package rootfs

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// TestDirTreeChanges checks each change a container makes to a volume
// against the directory on the host: a file made, written at an offset,
// cut short, given a mode, renamed over another, linked to and removed,
// and a directory made and removed. The tree is a temporary directory.
func TestDirTreeChanges(t *testing.T) {
	dir := t.TempDir()
	tree, err := OpenDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	onHost := func(name string) string {
		t.Helper()
		content, err := os.ReadFile(filepath.Join(dir, name))
		must(err)
		return string(content)
	}

	// A file, written in two pieces, the second past the first.
	must(tree.Create("/notes", 0o640))
	if err := tree.Create("/notes", 0o640); !errors.Is(err, fs.ErrExist) {
		t.Errorf("a second create: %v", err)
	}
	count, err := tree.Write("/notes", 0, []byte("hello"))
	if err != nil || count != 5 {
		t.Fatalf("write: %d, %v", count, err)
	}
	_, err = tree.Write("/notes", 5, []byte(", volume"))
	must(err)
	if got := onHost("notes"); got != "hello, volume" {
		t.Errorf("on the host: %q", got)
	}

	// Its size and mode.
	size, mode := int64(5), uint32(0o600)
	must(tree.SetAttr("/notes", SetAttr{Size: &size, Mode: &mode}))
	attr, err := tree.Attr("/notes")
	must(err)
	if attr.Size != 5 || attr.Mode != 0o600 || onHost("notes") != "hello" {
		t.Errorf("after the change: %+v, %q", attr, onHost("notes"))
	}

	// A directory, a rename into it, and a link.
	must(tree.Mkdir("/sub", 0o755))
	must(tree.Rename("/notes", "/sub/moved"))
	must(tree.Symlink("/sub/link", "moved"))
	if target, err := tree.Link("/sub/link"); err != nil || target != "moved" {
		t.Errorf("the link: %q, %v", target, err)
	}
	if onHost("sub/moved") != "hello" {
		t.Error("the renamed file lost its content")
	}

	// Removal insists on the kind, and a directory on being empty.
	if err := tree.Remove("/sub", false); err == nil {
		t.Error("a directory was removed as a file")
	}
	if err := tree.Remove("/sub", true); err == nil {
		t.Error("a directory that is not empty was removed")
	}
	must(tree.Remove("/sub/link", false))
	must(tree.Remove("/sub/moved", false))
	must(tree.Remove("/sub", true))
	if _, err := os.Stat(filepath.Join(dir, "sub")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the directory is still on the host: %v", err)
	}
}

// TestHandlerChanges checks changes as the page sends them, and that a
// handler told its tree is read-only refuses each with the errno a
// read-only file system gives. One server stands in for a volume and
// one for an image, over the same directory.
func TestHandlerChanges(t *testing.T) {
	dir := t.TempDir()
	tree, err := OpenDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()

	post := func(handler Handler, op string, params url.Values, body []byte) (int, map[string]any) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/vol/"+op+"?"+params.Encode(), bytes.NewReader(body))
		handler.Serve(recorder, request, op)
		var answer map[string]any
		json.NewDecoder(recorder.Body).Decode(&answer)
		return recorder.Code, answer
	}
	volume := Handler{Tree: tree, Writable: true}
	image := Handler{Tree: tree}

	// A volume takes a file and a write.
	if status, _ := post(volume, OpCreate, url.Values{"p": {"/f"}, "m": {"644"}}, nil); status != http.StatusOK {
		t.Fatalf("create: %d", status)
	}
	status, answer := post(volume, OpWrite, url.Values{"p": {"/f"}, "o": {"0"}}, []byte("data"))
	if status != http.StatusOK || answer["written"] != float64(4) {
		t.Errorf("write: %d %v", status, answer)
	}
	if content, _ := os.ReadFile(filepath.Join(dir, "f")); string(content) != "data" {
		t.Errorf("on the host: %q", content)
	}

	// An image takes neither, and a volume takes no path that leaves it.
	status, answer = post(image, OpWrite, url.Values{"p": {"/f"}, "o": {"0"}}, []byte("x"))
	if status != http.StatusForbidden || answer["code"] != codeReadOnly {
		t.Errorf("a write to an image: %d %v", status, answer)
	}
	status, answer = post(volume, OpCreate, url.Values{"p": {"/../escaped"}, "m": {"644"}}, nil)
	if status == http.StatusOK {
		t.Errorf("a create outside the volume: %d %v", status, answer)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escaped")); err == nil {
		t.Error("a file was made outside the volume")
	}
}
