package main

import (
	"reflect"
	"testing"
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
