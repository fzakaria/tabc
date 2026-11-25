package rootfs

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// newTree makes a small root file system in a directory: a script, a
// directory, a link inside the tree, a link that points out of it, and
// a secret next to the root that nothing in the root may reach. It
// returns the server and a function that performs one operation.
func newTree(t *testing.T) func(op string, params url.Values) *http.Response {
	t.Helper()
	outside := t.TempDir()
	root := filepath.Join(outside, "root")

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, "bin"), 0o755))
	must(os.WriteFile(filepath.Join(root, "bin", "hello"), []byte("#!/bin/sh\necho hi\n"), 0o755))
	must(os.Symlink("hello", filepath.Join(root, "bin", "alias")))
	must(os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0o600))
	must(os.Symlink("../secret", filepath.Join(root, "escape")))

	tree, err := OpenDir(root)
	must(err)
	t.Cleanup(func() { tree.Close() })
	server := Handler{Tree: tree}

	return func(op string, params url.Values) *http.Response {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/fs/"+op+"?"+params.Encode(), nil)
		server.Serve(recorder, request, op)
		return recorder.Result()
	}
}

// decode reads a JSON answer into value.
func decode(t *testing.T, response *http.Response, value any) {
	t.Helper()
	if err := json.NewDecoder(response.Body).Decode(value); err != nil {
		t.Fatal(err)
	}
}

// TestAttr checks the attributes of the root, a file and a link, and
// that a missing path answers ENOENT, by asking the server for each.
func TestAttr(t *testing.T) {
	call := newTree(t)

	var attr Attr
	decode(t, call(OpAttr, url.Values{"p": {"/"}}), &attr)
	if attr.Type != TypeDir {
		t.Errorf("the root is a %q", attr.Type)
	}

	decode(t, call(OpAttr, url.Values{"p": {"/bin/hello"}}), &attr)
	if attr.Type != TypeFile || attr.Size != 18 || attr.Mode != 0o755 || attr.Ino == 0 {
		t.Errorf("the file: %+v", attr)
	}

	decode(t, call(OpAttr, url.Values{"p": {"/bin/alias"}}), &attr)
	if attr.Type != TypeSymlink {
		t.Errorf("the link is a %q", attr.Type)
	}

	response := call(OpAttr, url.Values{"p": {"/bin/nope"}})
	var failed failure
	decode(t, response, &failed)
	if response.StatusCode != http.StatusNotFound || failed.Code != codeNotFound {
		t.Errorf("a missing path: %d %q", response.StatusCode, failed.Code)
	}
}

// TestDirAndLink checks a directory's listing and a link's target.
func TestDirAndLink(t *testing.T) {
	call := newTree(t)

	var entries []Entry
	decode(t, call(OpDir, url.Values{"p": {"/bin"}}), &entries)
	kinds := map[string]string{}
	for _, entry := range entries {
		kinds[entry.Name] = entry.Type

		// Each entry carries what a lookup of the name would ask next.
		if entry.Name == "hello" && (entry.Attr.Size != 18 || entry.Attr.Mode != 0o755) {
			t.Errorf("the file's attributes in the listing: %+v", entry.Attr)
		}
		if entry.Name == "alias" && entry.Target != "hello" {
			t.Errorf("the link's target in the listing: %q", entry.Target)
		}
	}
	if len(kinds) != 2 || kinds["hello"] != TypeFile || kinds["alias"] != TypeSymlink {
		t.Errorf("the listing: %v", kinds)
	}

	var link map[string]string
	decode(t, call(OpLink, url.Values{"p": {"/bin/alias"}}), &link)
	if link["target"] != "hello" {
		t.Errorf("the link's target: %q", link["target"])
	}
}

// TestRead checks a range read, a read that runs past the end, and one
// that starts past the end.
func TestRead(t *testing.T) {
	call := newTree(t)

	read := func(offset, length string) string {
		response := call(OpRead, url.Values{"p": {"/bin/hello"}, "o": {offset}, "n": {length}})
		if response.StatusCode != http.StatusOK {
			t.Fatalf("read %s+%s: status %d", offset, length, response.StatusCode)
		}
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}

	if got := read("2", "7"); got != "/bin/sh" {
		t.Errorf("a range: %q", got)
	}
	if got := read("10", "100"); got != "echo hi\n" {
		t.Errorf("a read past the end: %q", got)
	}
	if got := read("100", "10"); got != "" {
		t.Errorf("a read from past the end: %q", got)
	}
}

// TestEscape checks that nothing outside the root can be read: not
// through "..", and not through a link in the root that points out of
// it, which the guest would never ask to be followed but a page that
// was taken over might.
func TestEscape(t *testing.T) {
	call := newTree(t)

	for _, path := range []string{"/../secret", "/bin/../../secret", "/escape"} {
		response := call(OpRead, url.Values{"p": {path}, "o": {"0"}, "n": {"100"}})
		body, _ := io.ReadAll(response.Body)
		if response.StatusCode == http.StatusOK || string(body) == "secret" {
			t.Errorf("read of %q got out: %d %q", path, response.StatusCode, body)
		}
	}

	// The link itself is still a link the guest may see.
	var attr Attr
	decode(t, call(OpAttr, url.Values{"p": {"/escape"}}), &attr)
	if attr.Type != TypeSymlink {
		t.Errorf("the escaping link is a %q", attr.Type)
	}
}
