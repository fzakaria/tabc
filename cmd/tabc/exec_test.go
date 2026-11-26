package main

import (
	"reflect"
	"testing"
)

// TestParseExec checks how exec's command line is read: flags up to the
// container's id, and every word after the id as the program's own,
// flags included. Each case parses one command line.
func TestParseExec(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want execOptions
	}{
		{
			name: "a program with flags of its own",
			args: []string{"web", "sh", "-c", "echo hi"},
			want: execOptions{id: "web", command: []string{"sh", "-c", "echo hi"}},
		},
		{
			name: "a terminal, as one cluster",
			args: []string{"-it", "web", "sh"},
			want: execOptions{id: "web", command: []string{"sh"}, terminal: true, interactive: true},
		},
		{
			name: "environment, directory and user",
			args: []string{"-e", "A=1", "--env=B=2", "-w", "/tmp", "-u", "5:6", "web", "env"},
			want: execOptions{
				id: "web", command: []string{"env"}, env: []string{"A=1", "B=2"}, cwd: "/tmp", user: "5:6",
			},
		},
		{
			name: "an engine's call",
			args: []string{
				"--process", "/p.json", "--console-socket", "/c.sock", "--pid-file", "/pid",
				"--detach", "--cap", "CAP_X", "--no-new-privs", "abc",
			},
			want: execOptions{
				id: "abc", process: "/p.json", consoleSocket: "/c.sock", pidFile: "/pid", detach: true,
			},
		},
	}
	for _, c := range cases {
		got, err := parseExec(c.args)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}

	for _, bad := range [][]string{{}, {"web"}, {"-e"}, {"--process"}} {
		if _, err := parseExec(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// TestParseExecUser checks that a user is read as a number, or two with
// a colon between, and that a name is refused, since exec has no passwd
// file of the container's at hand to look a name up in.
func TestParseExecUser(t *testing.T) {
	user, err := parseExecUser("5:6")
	if err != nil || user.UID != 5 || user.GID != 6 {
		t.Errorf("5:6 read as %+v, %v", user, err)
	}
	user, err = parseExecUser("7")
	if err != nil || user.UID != 7 || user.GID != 0 {
		t.Errorf("7 read as %+v, %v", user, err)
	}
	if _, err := parseExecUser("www-data"); err == nil {
		t.Error("a name was accepted")
	}
}
