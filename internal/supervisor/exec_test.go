package supervisor

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/fzakaria/tabc/internal/guest"
)

// TestFrames checks that frames written one after another are read back
// as they were written, kind and bytes, an empty one included, by
// writing three to a buffer and reading the buffer to its end.
func TestFrames(t *testing.T) {
	var wire bytes.Buffer
	sent := []struct {
		kind    byte
		payload string
	}{
		{frameStdout, "hello"},
		{frameStdin, ""},
		{frameControl, `{"t":"exit","status":3}`},
	}
	for _, frame := range sent {
		if err := writeFrame(&wire, frame.kind, []byte(frame.payload)); err != nil {
			t.Fatal(err)
		}
	}

	for _, want := range sent {
		kind, payload, err := readFrame(&wire)
		if err != nil {
			t.Fatal(err)
		}
		if kind != want.kind || string(payload) != want.payload {
			t.Errorf("read frame %d %q, want %d %q", kind, payload, want.kind, want.payload)
		}
	}
	if _, _, err := readFrame(&wire); err == nil {
		t.Error("reading past the last frame did not fail")
	}
}

// TestFrameTooLarge checks that a frame claiming more bytes than a
// frame may hold is refused before any of them is read, by handing
// readFrame a header alone.
func TestFrameTooLarge(t *testing.T) {
	header := []byte{frameStdout, 0xff, 0xff, 0xff, 0xff}
	if _, _, err := readFrame(bytes.NewReader(header)); err == nil {
		t.Error("an oversized frame was accepted")
	}
}

// TestExecSpec checks what a second program is told about itself: the
// container's environment, directory and user where the request names
// none, and the request's own where it does. Each case builds the
// guest's spec from one container and one request.
func TestExecSpec(t *testing.T) {
	container := guest.Spec{
		Args:           []string{"server"},
		Env:            []string{"PATH=/bin", "MODE=prod"},
		Cwd:            "/srv",
		UID:            100,
		GID:            101,
		AdditionalGids: []uint32{7},
		Hostname:       "web",
		Files:          map[string]string{"/etc/hosts": "x"},
	}

	cases := []struct {
		name    string
		request ExecRequest
		want    guest.Spec
	}{
		{
			name:    "the container's own",
			request: ExecRequest{Args: []string{"ps"}},
			want: guest.Spec{
				Args: []string{"ps"}, Env: []string{"PATH=/bin", "MODE=prod"}, Cwd: "/srv",
				UID: 100, GID: 101, AdditionalGids: []uint32{7},
			},
		},
		{
			name:    "more environment",
			request: ExecRequest{Args: []string{"ps"}, Env: []string{"DEBUG=1"}},
			want: guest.Spec{
				Args: []string{"ps"}, Env: []string{"PATH=/bin", "MODE=prod", "DEBUG=1"}, Cwd: "/srv",
				UID: 100, GID: 101, AdditionalGids: []uint32{7},
			},
		},
		{
			name: "the request's own",
			request: ExecRequest{
				Args: []string{"id"}, Env: []string{"ONLY=1"}, Environment: EnvironmentReplace,
				Cwd: "/tmp", User: &ExecUser{UID: 0, GID: 0},
			},
			want: guest.Spec{Args: []string{"id"}, Env: []string{"ONLY=1"}, Cwd: "/tmp"},
		},
		{
			// A user by name is the guest's to look up, and takes the
			// place of the container's user and of its extra groups.
			name:    "a user by name",
			request: ExecRequest{Args: []string{"id"}, UserName: "app:staff"},
			want: guest.Spec{
				Args: []string{"id"}, Env: []string{"PATH=/bin", "MODE=prod"}, Cwd: "/srv",
				User: "app:staff",
			},
		},
	}
	for _, c := range cases {
		got, err := execSpec(container, c.request)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}

	if _, err := execSpec(container, ExecRequest{}); err == nil {
		t.Error("a request with no program was accepted")
	}
}
