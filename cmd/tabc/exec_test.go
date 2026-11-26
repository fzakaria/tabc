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

// TestExecRequestUser checks that the user of `exec -u` goes to the
// supervisor as written, a name as much as a number: the name is looked
// up inside the container, where the passwd file of the moment is.
func TestExecRequestUser(t *testing.T) {
	for _, user := range []string{"5:6", "www-data", "www-data:staff"} {
		request, err := execRequest(execOptions{command: []string{"id"}, user: user})
		if err != nil {
			t.Fatalf("%q: %v", user, err)
		}
		if request.UserName != user || request.User != nil {
			t.Errorf("%q went as name %q, numbers %+v", user, request.UserName, request.User)
		}
	}
}
