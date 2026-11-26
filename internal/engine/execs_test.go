package engine

import (
	"reflect"
	"testing"

	"github.com/fzakaria/tabc/internal/supervisor"
)

// TestExecRequest checks what the supervisor is asked for a second
// program: the program, its extra environment and directory as given,
// the terminal's type added when the program gets a terminal, and the
// user as written, for the guest to look up. Each case turns one description into a
// request.
func TestExecRequest(t *testing.T) {
	cases := []struct {
		name string
		spec ExecSpec
		want supervisor.ExecRequest
	}{
		{
			name: "a plain program",
			spec: ExecSpec{Cmd: []string{"ps"}, Env: []string{"A=1"}, WorkingDir: "/tmp"},
			want: supervisor.ExecRequest{Args: []string{"ps"}, Env: []string{"A=1"}, Cwd: "/tmp"},
		},
		{
			name: "a shell on a terminal, as a user",
			spec: ExecSpec{Cmd: []string{"sh"}, User: "app:staff", Tty: true, Cols: 100, Rows: 30},
			want: supervisor.ExecRequest{
				Args: []string{"sh"}, Env: []string{envTerm}, UserName: "app:staff",
				Terminal: true, Cols: 100, Rows: 30,
			},
		},
	}
	for _, c := range cases {
		got := execRequest(c.spec)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}
