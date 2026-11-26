package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/fzakaria/tabc/internal/plan"
)

// TestParseImageRun checks that flags before the image are read, in
// their short, long and "=" forms, and that everything after the image
// is the program's, flags included.
func TestParseImageRun(t *testing.T) {
	parsed, err := parseImageRun([]string{
		"-it", "--rm", "-e", "A=1", "--env=B=2", "-w", "/srv", "--user=app", "-m", "512m",
		"alpine:3.22", "ls", "-la", "--color",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := imageRun{
		interactive: true,
		terminal:    true,
		env:         []string{"A=1", "B=2"},
		cwd:         "/srv",
		user:        "app",
		memory:      512 << 20,
		image:       "alpine:3.22",
		command:     []string{"ls", "-la", "--color"},
	}
	if !reflect.DeepEqual(parsed, want) {
		t.Errorf("got %+v\nwant %+v", parsed, want)
	}
}

// TestParseImageRunErrors checks the three ways the words can be wrong:
// no image, a flag tabc does not have, and a flag missing its value.
func TestParseImageRunErrors(t *testing.T) {
	for _, args := range [][]string{{}, {"-i"}, {"--privileged", "alpine"}, {"-e"}} {
		if _, err := parseImageRun(args); err == nil {
			t.Errorf("%v parsed", args)
		}
	}
}

// TestParseMemory checks each unit and a bare number of bytes, and that
// text that is not a size is refused.
func TestParseMemory(t *testing.T) {
	cases := map[string]int64{"1024": 1024, "64k": 64 << 10, "512m": 512 << 20, "2G": 2 << 30}
	for text, want := range cases {
		got, err := parseMemory(text)
		if err != nil || got != want {
			t.Errorf("parseMemory(%q) = %d, %v; want %d", text, got, err, want)
		}
	}
	for _, text := range []string{"", "m", "-1g", "lots"} {
		if _, err := parseMemory(text); err == nil {
			t.Errorf("parseMemory(%q) succeeded", text)
		}
	}
}

// TestParsePublish checks the three ways a published port is written,
// and that text that is not one is refused.
func TestParsePublish(t *testing.T) {
	cases := map[string]plan.Publish{
		"80":                {HostPort: 80, GuestPort: 80},
		"8080:80":           {HostPort: 8080, GuestPort: 80},
		"127.0.0.1:8080:80": {HostIP: "127.0.0.1", HostPort: 8080, GuestPort: 80},
		"0.0.0.0:9000:9000": {HostIP: "0.0.0.0", HostPort: 9000, GuestPort: 9000},
	}
	for text, want := range cases {
		got, err := parsePublish(text)
		if err != nil || got != want {
			t.Errorf("parsePublish(%q) = %+v, %v; want %+v", text, got, err, want)
		}
	}
	for _, text := range []string{"", "web", "0:80", "1:2:3:4", "80:99999"} {
		if _, err := parsePublish(text); err == nil {
			t.Errorf("parsePublish(%q) succeeded", text)
		}
	}
}

// TestParseVolume checks a mount's two forms against a real directory,
// and that a missing directory, a file, and a target that is not a path
// are refused.
func TestParseVolume(t *testing.T) {
	dir := t.TempDir()

	volume, err := parseVolume(dir + ":/data")
	if err != nil || volume.Source != dir || volume.Target != "/data" || volume.ReadOnly {
		t.Errorf("read-write: %+v, %v", volume, err)
	}
	volume, err = parseVolume(dir + ":/data/:ro")
	if err != nil || volume.Target != "/data" || !volume.ReadOnly {
		t.Errorf("read-only: %+v, %v", volume, err)
	}

	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{dir, dir + ":data", dir + "/missing:/data", file + ":/data", ":/data"} {
		if _, err := parseVolume(text); err == nil {
			t.Errorf("parseVolume(%q) succeeded", text)
		}
	}
}
