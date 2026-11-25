package image

import (
	"io/fs"
	"reflect"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/fzakaria/tabc/internal/rootfs"
)

// fakeTree is an image's file system holding only the files a test
// gives it, which is all that looking up a user reads.
type fakeTree map[string]string

func (t fakeTree) Attr(string) (rootfs.Attr, error)   { return rootfs.Attr{}, fs.ErrNotExist }
func (t fakeTree) Dir(string) ([]rootfs.Entry, error) { return nil, fs.ErrNotExist }
func (t fakeTree) Link(string) (string, error)        { return "", fs.ErrNotExist }
func (t fakeTree) Close() error                       { return nil }
func (t fakeTree) Read(path string, _, _ int64) ([]byte, error) {
	content, has := t[path]
	if !has {
		return nil, fs.ErrNotExist
	}
	return []byte(content), nil
}

// TestSpecFromImage checks the program and environment a container gets
// from an image's configuration alone: the entry point before the
// default arguments, the image's environment and directory, and the
// files every container is given.
func TestSpecFromImage(t *testing.T) {
	config := v1.Config{
		Entrypoint: []string{"/entry"},
		Cmd:        []string{"serve"},
		Env:        []string{"PATH=/app/bin", "MODE=prod"},
		WorkingDir: "/app",
	}
	spec, err := Spec(config, Overrides{}, fakeTree{})
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(spec.Args, []string{"/entry", "serve"}) {
		t.Errorf("program %v", spec.Args)
	}
	if !reflect.DeepEqual(spec.Env, []string{"PATH=/app/bin", "MODE=prod"}) || spec.Cwd != "/app" {
		t.Errorf("environment %v in %q", spec.Env, spec.Cwd)
	}
	if spec.UID != 0 || spec.GID != 0 {
		t.Errorf("user %d:%d, want the superuser", spec.UID, spec.GID)
	}
	if spec.Files["/etc/hosts"] != hostsContent || spec.Files["/etc/resolv.conf"] != resolvContent {
		t.Errorf("files %v", spec.Files)
	}
}

// TestSpecOverrides checks that a command replaces the default
// arguments and keeps the entry point, that the command line's
// environment comes last so it wins, and that an image with no PATH
// gets one.
func TestSpecOverrides(t *testing.T) {
	config := v1.Config{Entrypoint: []string{"/entry"}, Cmd: []string{"serve"}, Env: []string{"A=1"}}
	spec, err := Spec(config, Overrides{
		Command: []string{"echo", "hi"},
		Env:     []string{"A=2"},
		Cwd:     "/tmp",
	}, fakeTree{})
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(spec.Args, []string{"/entry", "echo", "hi"}) {
		t.Errorf("program %v", spec.Args)
	}
	if !reflect.DeepEqual(spec.Env, []string{"A=1", defaultPath, "A=2"}) || spec.Cwd != "/tmp" {
		t.Errorf("environment %v in %q", spec.Env, spec.Cwd)
	}

	if _, err := Spec(v1.Config{}, Overrides{}, fakeTree{}); err == nil {
		t.Error("an image with no program and no command made a spec")
	}
}

// TestResolveUser checks each way an image names its user: by number,
// by name with the primary group from the passwd file, and with a group
// by number or by name. The databases are two small files in a fake
// image.
func TestResolveUser(t *testing.T) {
	tree := fakeTree{
		"/etc/passwd": "root:x:0:0:root:/root:/bin/sh\napp:x:1000:1001:App:/home/app:/bin/sh\n",
		"/etc/group":  "root:x:0:\nstaff:x:50:app\n",
	}

	cases := []struct {
		user     string
		uid, gid uint32
	}{
		{"", 0, 0},
		{"1234", 1234, 0},
		{"1234:5678", 1234, 5678},
		{"app", 1000, 1001},
		{"app:staff", 1000, 50},
		{"app:7", 1000, 7},
		{"0:staff", 0, 50},
	}
	for _, c := range cases {
		uid, gid, err := resolveUser(c.user, tree)
		if err != nil {
			t.Errorf("resolveUser(%q): %v", c.user, err)
			continue
		}
		if uid != c.uid || gid != c.gid {
			t.Errorf("resolveUser(%q) = %d:%d, want %d:%d", c.user, uid, gid, c.uid, c.gid)
		}
	}

	for _, user := range []string{"nobody-here", "app:nogroup"} {
		if _, _, err := resolveUser(user, tree); err == nil {
			t.Errorf("resolveUser(%q) succeeded", user)
		}
	}
}
