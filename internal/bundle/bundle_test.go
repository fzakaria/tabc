package bundle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	specs "github.com/opencontainers/runtime-spec/specs-go"
)

// writeBundle makes a bundle directory holding spec and an empty root
// file system, and returns the directory.
func writeBundle(t *testing.T, spec specs.Spec) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "rootfs"), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestGuestSpec checks what the guest is told for a container an engine
// configured the way Docker does: the program, environment, directory
// and user carried over, a bound hosts file copied by content, the
// engine's resolver file replaced by the guest's, and a bound directory
// left out. The bundle is written to disk and loaded as an engine's
// would be.
func TestGuestSpec(t *testing.T) {
	sources := t.TempDir()
	hosts := filepath.Join(sources, "hosts")
	resolv := filepath.Join(sources, "resolv.conf")
	if err := os.WriteFile(hosts, []byte("127.0.0.1 localhost\n172.17.0.2 box\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resolv, []byte("nameserver 192.168.1.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dir := writeBundle(t, specs.Spec{
		Hostname: "box",
		Process: &specs.Process{
			Args: []string{"echo", "hi"},
			Env:  []string{"PATH=/bin", "A=b"},
			Cwd:  "/srv",
			User: specs.User{UID: 1000, GID: 1000, AdditionalGids: []uint32{10}},
		},
		Mounts: []specs.Mount{
			{Destination: "/proc", Type: "proc", Source: "proc"},
			{Destination: "/etc/hosts", Type: "bind", Source: hosts, Options: []string{"rbind"}},
			{Destination: "/etc/resolv.conf", Type: "bind", Source: resolv},
			{Destination: "/data", Type: "bind", Source: sources},
		},
	})

	b, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	spec := b.GuestSpec(NetworkOn)

	if len(spec.Args) != 2 || spec.Args[0] != "echo" || spec.Cwd != "/srv" {
		t.Errorf("program %v in %q", spec.Args, spec.Cwd)
	}
	if spec.UID != 1000 || spec.GID != 1000 || len(spec.AdditionalGids) != 1 {
		t.Errorf("user %d:%d %v", spec.UID, spec.GID, spec.AdditionalGids)
	}
	if spec.Files["/etc/hosts"] != "127.0.0.1 localhost\n172.17.0.2 box\n" {
		t.Errorf("hosts file %q", spec.Files["/etc/hosts"])
	}
	if spec.Files["/etc/resolv.conf"] != guestResolver {
		t.Errorf("resolver file %q, want the guest's", spec.Files["/etc/resolv.conf"])
	}
	if spec.Files["/etc/hostname"] != "box\n" {
		t.Errorf("hostname file %q", spec.Files["/etc/hostname"])
	}
	if _, bound := spec.Files["/data"]; bound {
		t.Error("a bound directory was copied as a file")
	}

	// The engine's resolver file is named, not copied: the engine may
	// write it after the container is created.
	if got := b.ResolvConfPath(); got != resolv {
		t.Errorf("resolver file %q, want %q", got, resolv)
	}
	if got := b.Plan().ResolvConfPath; got != resolv {
		t.Errorf("the plan's resolver file %q, want %q", got, resolv)
	}

	// With no network there is nothing to resolve with.
	if _, has := b.GuestSpec(NetworkOff).Files["/etc/resolv.conf"]; has {
		t.Error("a container with no network got a resolver file")
	}
}

// TestGuestSpecDefaults checks a bare configuration, as `runc spec`
// writes one: the directory defaults to the root and the container
// still gets localhost by name.
func TestGuestSpecDefaults(t *testing.T) {
	dir := writeBundle(t, specs.Spec{Process: &specs.Process{Args: []string{"sh"}}})
	b, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	spec := b.GuestSpec(NetworkOff)
	if spec.Cwd != "/" {
		t.Errorf("directory %q, want /", spec.Cwd)
	}
	if spec.Files["/etc/hosts"] != defaultHosts {
		t.Errorf("hosts file %q", spec.Files["/etc/hosts"])
	}
}

// TestLoadErrors checks that a bundle with no program, and one whose
// root file system is missing, are refused when loaded.
func TestLoadErrors(t *testing.T) {
	if _, err := Load(writeBundle(t, specs.Spec{})); err == nil {
		t.Error("a bundle with no program loaded")
	}

	dir := writeBundle(t, specs.Spec{
		Process: &specs.Process{Args: []string{"sh"}},
		Root:    &specs.Root{Path: "missing"},
	})
	if _, err := Load(dir); err == nil {
		t.Error("a bundle with no root file system loaded")
	}
}

// TestTerminalAndLimits checks the two settings the supervisor reads
// besides the program: whether a terminal is wanted and at what size,
// and the memory limit.
func TestTerminalAndLimits(t *testing.T) {
	limit := int64(512 << 20)
	dir := writeBundle(t, specs.Spec{
		Process: &specs.Process{
			Args:        []string{"sh"},
			Terminal:    true,
			ConsoleSize: &specs.Box{Width: 120, Height: 40},
		},
		Linux: &specs.Linux{
			Resources:  &specs.LinuxResources{Memory: &specs.LinuxMemory{Limit: &limit}},
			Namespaces: []specs.LinuxNamespace{{Type: specs.NetworkNamespace, Path: "/run/netns/x"}},
		},
	})
	b, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	wanted, cols, rows := b.Terminal()
	if !wanted || cols != 120 || rows != 40 {
		t.Errorf("terminal %v %dx%d", wanted, cols, rows)
	}
	if b.MemoryLimit() != limit {
		t.Errorf("memory limit %d", b.MemoryLimit())
	}
	if path, wanted := b.NetworkNamespace(); !wanted || path != "/run/netns/x" {
		t.Errorf("network namespace %q, wanted %v", path, wanted)
	}
}
