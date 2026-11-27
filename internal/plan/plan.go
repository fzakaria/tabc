// Package plan is a container as the supervisor runs it: the program,
// where the root file system comes from, the terminal and the memory.
// Whoever makes the container writes the plan into the container's
// state directory, from an OCI bundle for an engine (internal/bundle)
// or from an image for `tabc run` (internal/image), and the supervisor
// reads the plan and needs to know nothing about either.
package plan

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/fzakaria/tabc/internal/guest"
)

// Terminal is how the program's terminal reaches whoever is at the
// other end.
type Terminal string

const (
	// TerminalNone: the program runs on pipes, the supervisor's own
	// stdin, stdout and stderr.
	TerminalNone Terminal = "none"
	// TerminalConsole: an engine waits on a console socket for the
	// controlling end of a pty.
	TerminalConsole Terminal = "console"
	// TerminalInherit: the supervisor's own stdin and stdout are a
	// terminal someone sits at, already in raw mode.
	TerminalInherit Terminal = "inherit"
)

// Browser is who loads the container's page.
type Browser string

const (
	// BrowserHeadless: a headless Chromium the supervisor starts for
	// this container alone, with a profile of its own. The default.
	BrowserHeadless Browser = ""
	// BrowserOpen: the user's own browser, which the supervisor asks
	// the desktop to open on the page.
	BrowserOpen Browser = "open"
	// BrowserLink: the user's own browser, when the user opens the
	// address the supervisor prints.
	BrowserLink Browser = "link"
	// BrowserExternal: whoever made the container loads the page
	// themselves, from the address in the container's record.
	BrowserExternal Browser = "external"
)

// fileName is the plan's file in a container's state directory, and
// fileMode its mode: the plan may hold an environment with secrets in
// it.
const (
	fileName = "plan.json"
	fileMode = 0o600
)

// Plan is one container.
type Plan struct {
	// Spec is what the guest's init is told.
	Spec guest.Spec `json:"spec"`

	// Terminal says whether the program gets a terminal and how, and
	// Cols and Rows its first size, 0 for the default.
	Terminal Terminal `json:"terminal"`
	Cols     int      `json:"cols,omitempty"`
	Rows     int      `json:"rows,omitempty"`

	// MemoryBytes is the container's memory limit, 0 for none.
	MemoryBytes int64 `json:"memoryBytes,omitempty"`

	// CPUs is how many CPUs the container asked for, 0 for no limit.
	CPUs int `json:"cpus,omitempty"`

	// The root file system: a directory on the host, or an image's
	// layers as uncompressed tar files, lowest first. One or the other.
	Rootfs string   `json:"rootfs,omitempty"`
	Layers []string `json:"layers,omitempty"`

	// ResolvConfPath is a file of resolver configuration naming the
	// servers to ask for the guest's lookups, empty for the host's own.
	// A path and not the file's content: an engine may write the file
	// after the container is created, once the container's network is
	// attached, and Docker does.
	ResolvConfPath string `json:"resolvConfPath,omitempty"`

	// Publish are ports of the host forwarded to ports of the guest.
	Publish []Publish `json:"publish,omitempty"`

	// Networks are the tabc networks the container is on, for
	// containers no engine gave a network (internal/netreg).
	Networks []Network `json:"networks,omitempty"`

	// Volumes are directories of the host carried into the container,
	// live and writable unless read-only.
	Volumes []Volume `json:"volumes,omitempty"`

	// Browser is who loads the container's page, and Title what the
	// page calls the container.
	Browser Browser `json:"browser,omitempty"`
	Title   string  `json:"title,omitempty"`

	// Movable has the program run where the guest can keep it through a
	// snapshot, so that the container can be parked and resumed in
	// another browser tab (`tabc park`). A program with a terminal runs
	// no differently. A program without one is given a terminal the
	// guest keeps, in raw mode, in place of its three pipes: its error
	// output arrives with its output, and the end of its input does not
	// reach it.
	Movable bool `json:"movable,omitempty"`
}

// Network is one tabc network a container is on, and the names other
// containers on the network reach the container by.
type Network struct {
	Name  string   `json:"name"`
	Names []string `json:"names"`
}

// Volume is one directory of the host mounted in the container.
type Volume struct {
	// Source is the directory on the host, and Target the path in the
	// container.
	Source string `json:"source"`
	Target string `json:"target"`
	// ReadOnly keeps the container from changing the directory.
	ReadOnly bool `json:"readOnly,omitempty"`
}

// Publish is one port of the host forwarded to a port of the guest.
type Publish struct {
	// HostIP is the address to listen on, empty for loopback.
	HostIP string `json:"hostIP,omitempty"`
	// HostPort is the port to listen on, and GuestPort the port in the
	// guest connections go to.
	HostPort  int `json:"hostPort"`
	GuestPort int `json:"guestPort"`
}

// Save writes the plan into a container's state directory.
func Save(dir string, p Plan) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, fileName), data, fileMode)
}

// Load reads the plan from a container's state directory.
func Load(dir string) (Plan, error) {
	var p Plan
	data, err := os.ReadFile(filepath.Join(dir, fileName))
	if err != nil {
		return p, err
	}
	err = json.Unmarshal(data, &p)
	return p, err
}

// NanoCPU is one CPU in the unit the Docker API counts CPUs in.
const NanoCPU = 1_000_000_000

// CPUs returns how many CPUs a limit on CPU time comes to: part of
// every whole, rounded up, since a guest has a whole number of CPUs.
// The pair is a quota and its period, or a count of NanoCPU and
// NanoCPU. A part or a whole that is not positive is no limit, 0.
func CPUs(part, whole int64) int {
	if part <= 0 || whole <= 0 {
		return 0
	}
	return int((part + whole - 1) / whole)
}
