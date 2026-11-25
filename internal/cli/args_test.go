package cli

import (
	"reflect"
	"testing"
)

// TestParseCreate checks the call containerd's shim makes to create a
// container: global flags before the command, the command's own flags
// after it in both "--name value" and "--name=value" form, switches
// tabc does not know, and the id last.
func TestParseCreate(t *testing.T) {
	inv, err := Parse([]string{
		"--root", "/run/containerd/runc/moby",
		"--log=/run/log.json",
		"--log-format", "json",
		"--systemd-cgroup",
		"create",
		"--bundle", "/run/bundle",
		"--pid-file=/run/init.pid",
		"--console-socket", "/run/console.sock",
		"--no-pivot",
		"abc123",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := Invocation{
		Root:          "/run/containerd/runc/moby",
		Log:           "/run/log.json",
		LogFormat:     "json",
		Command:       "create",
		Bundle:        "/run/bundle",
		PidFile:       "/run/init.pid",
		ConsoleSocket: "/run/console.sock",
		Args:          []string{"abc123"},
	}
	if !reflect.DeepEqual(inv, want) {
		t.Errorf("got %+v\nwant %+v", inv, want)
	}
}

// TestParseKill checks that `kill` keeps the id and the signal as its
// arguments in order, and reads --all, by parsing the two calls an
// engine makes.
func TestParseKill(t *testing.T) {
	inv, err := Parse([]string{"kill", "--all", "abc123", "KILL"})
	if err != nil {
		t.Fatal(err)
	}
	if !inv.All || !reflect.DeepEqual(inv.Args, []string{"abc123", "KILL"}) {
		t.Errorf("got %+v", inv)
	}

	inv, err = Parse([]string{"kill", "abc123", "9"})
	if err != nil {
		t.Fatal(err)
	}
	if inv.All || !reflect.DeepEqual(inv.Args, []string{"abc123", "9"}) {
		t.Errorf("got %+v", inv)
	}
}

// TestParseErrors checks that a value flag with nothing after it is an
// error, and that a command without an id says so when the id is asked
// for.
func TestParseErrors(t *testing.T) {
	if _, err := Parse([]string{"create", "--bundle"}); err == nil {
		t.Error("a --bundle with no value parsed")
	}

	inv, err := Parse([]string{"delete", "--force"})
	if err != nil {
		t.Fatal(err)
	}
	if !inv.Force {
		t.Error("--force was not read")
	}
	if _, err := inv.ID(); err == nil {
		t.Error("a delete with no id gave one")
	}
}

// TestParseVersion checks that --version alone is recognised, since
// engines probe a runtime with it.
func TestParseVersion(t *testing.T) {
	inv, err := Parse([]string{"--version"})
	if err != nil {
		t.Fatal(err)
	}
	if !inv.Version || inv.Command != "" {
		t.Errorf("got %+v", inv)
	}
}
