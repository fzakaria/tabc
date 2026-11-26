package users

import (
	"errors"
	"os"
	"testing"
)

// TestResolve checks each way a user is written: by number, by name
// with the primary group from the passwd file, and with a group by
// number or by name, and that a name in neither file is refused. The
// databases are two small files handed over by a reader.
func TestResolve(t *testing.T) {
	files := map[string]string{
		PasswdPath: "root:x:0:0:root:/root:/bin/sh\napp:x:1000:1001:App:/home/app:/bin/sh\n",
		GroupPath:  "root:x:0:\nstaff:x:50:app\n",
	}
	read := func(path string) ([]byte, error) {
		content, has := files[path]
		if !has {
			return nil, os.ErrNotExist
		}
		return []byte(content), nil
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
		uid, gid, err := Resolve(c.user, read)
		if err != nil {
			t.Errorf("Resolve(%q): %v", c.user, err)
			continue
		}
		if uid != c.uid || gid != c.gid {
			t.Errorf("Resolve(%q) = %d:%d, want %d:%d", c.user, uid, gid, c.uid, c.gid)
		}
	}

	for _, user := range []string{"nobody-here", "app:nogroup"} {
		if _, _, err := Resolve(user, read); !errors.Is(err, ErrUnknown) {
			t.Errorf("Resolve(%q): %v, want ErrUnknown", user, err)
		}
	}

	// A number needs no file, so a container with no passwd file at all
	// still takes one.
	none := func(string) ([]byte, error) { return nil, os.ErrNotExist }
	if uid, gid, err := Resolve("5:6", none); err != nil || uid != 5 || gid != 6 {
		t.Errorf("Resolve(5:6) with no files = %d:%d, %v", uid, gid, err)
	}
}
