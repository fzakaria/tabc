// Package bundle reads an OCI bundle, the directory an engine hands a
// runtime: a config.json and the container's root file system.
package bundle

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	specs "github.com/opencontainers/runtime-spec/specs-go"

	"github.com/fzakaria/tabc/internal/guest"
	"github.com/fzakaria/tabc/internal/plan"
)

// configFile is the bundle's configuration, and defaultRootfs where the
// root file system is when the configuration does not say.
const (
	configFile    = "config.json"
	defaultRootfs = "rootfs"
)

// defaultCwd is where a program runs when the configuration names no
// directory.
const defaultCwd = "/"

// mountTypeBind is how the configuration marks a host path bound into
// the container, and optionBind the option that says the same for a
// mount with no type.
const (
	mountTypeBind = "bind"
	optionBind    = "bind"
	optionRbind   = "rbind"
	// optionReadOnly marks a mount the container may not change.
	optionReadOnly = "ro"
)

// maxBoundFile is the largest file a bind mount may name for tabc to
// copy it into the guest. Engines bind small generated files, and
// anything larger is a volume, which tabc does not carry.
const maxBoundFile = 1 << 20

// The names the guest's loopback goes by, written to a container whose
// engine bound no hosts file. A program in the guest that finds no
// hosts file asks DNS for localhost.
const (
	hostsPath     = "/etc/hosts"
	defaultHosts  = "127.0.0.1\tlocalhost\n"
	hostnamePath  = "/etc/hostname"
	resolvPath    = "/etc/resolv.conf"
	guestResolver = "nameserver 10.0.2.3\n"
)

// Network says whether the container has a network, which decides what
// its resolver configuration is.
type Network int

const (
	// NetworkOff: the guest has no network device.
	NetworkOff Network = iota
	// NetworkOn: the guest reaches the network through the supervisor.
	NetworkOn
)

// Bundle is a bundle as read from disk.
type Bundle struct {
	// Dir is the bundle's directory, absolute.
	Dir string
	// Rootfs is the container's root file system on the host, absolute.
	Rootfs string
	// Spec is the configuration.
	Spec *specs.Spec
}

// Load reads the bundle in dir.
func Load(dir string) (*Bundle, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}

	// The configuration.
	data, err := os.ReadFile(filepath.Join(dir, configFile))
	if err != nil {
		return nil, fmt.Errorf("reading the bundle's configuration: %w", err)
	}
	var spec specs.Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("%s: %w", configFile, err)
	}
	if spec.Process == nil || len(spec.Process.Args) == 0 {
		return nil, errors.New("the bundle names no program to run (process.args)")
	}

	// The root file system, relative to the bundle unless absolute.
	rootfs := defaultRootfs
	if spec.Root != nil && spec.Root.Path != "" {
		rootfs = spec.Root.Path
	}
	if !filepath.IsAbs(rootfs) {
		rootfs = filepath.Join(dir, rootfs)
	}
	info, err := os.Stat(rootfs)
	if err != nil {
		return nil, fmt.Errorf("the bundle's root file system: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("the bundle's root file system %s is not a directory", rootfs)
	}

	return &Bundle{Dir: dir, Rootfs: rootfs, Spec: &spec}, nil
}

// Terminal reports whether the program wants a terminal, and the
// terminal's size when the configuration gives one.
func (b *Bundle) Terminal() (wanted bool, cols, rows uint) {
	process := b.Spec.Process
	if !process.Terminal {
		return false, 0, 0
	}
	if process.ConsoleSize == nil {
		return true, 0, 0
	}
	return true, process.ConsoleSize.Width, process.ConsoleSize.Height
}

// MemoryLimit returns the container's memory limit in bytes, or 0 when
// the configuration sets none.
func (b *Bundle) MemoryLimit() int64 {
	linux := b.Spec.Linux
	if linux == nil || linux.Resources == nil || linux.Resources.Memory == nil {
		return 0
	}
	limit := linux.Resources.Memory.Limit
	if limit == nil || *limit <= 0 {
		return 0
	}
	return *limit
}

// CPUs returns how many CPUs the configuration's quota of CPU time
// comes to, or 0 when the configuration sets no quota.
func (b *Bundle) CPUs() int {
	linux := b.Spec.Linux
	if linux == nil || linux.Resources == nil || linux.Resources.CPU == nil {
		return 0
	}
	cpu := linux.Resources.CPU
	if cpu.Quota == nil || cpu.Period == nil {
		return 0
	}
	return plan.CPUs(*cpu.Quota, int64(*cpu.Period))
}

// GuestSpec returns what the guest's init needs to run the container's
// program.
func (b *Bundle) GuestSpec(network Network) guest.Spec {
	process := b.Spec.Process

	cwd := process.Cwd
	if cwd == "" {
		cwd = defaultCwd
	}

	spec := guest.Spec{
		Args:           process.Args,
		Env:            process.Env,
		Cwd:            cwd,
		UID:            process.User.UID,
		GID:            process.User.GID,
		AdditionalGids: process.User.AdditionalGids,
		Hostname:       b.Spec.Hostname,
		Files:          b.boundFiles(),
	}

	// A container needs localhost by name whether or not the engine
	// bound a hosts file, and a host name file to match its host name.
	if _, bound := spec.Files[hostsPath]; !bound {
		spec.Files[hostsPath] = defaultHosts
	}
	if _, bound := spec.Files[hostnamePath]; !bound && spec.Hostname != "" {
		spec.Files[hostnamePath] = spec.Hostname + "\n"
	}

	// The engine's resolver configuration names servers on the host's
	// network. The guest's network is its own, with its own resolver, and
	// a guest with no network has nothing to resolve with.
	delete(spec.Files, resolvPath)
	if network == NetworkOn {
		spec.Files[resolvPath] = guestResolver
	}

	return spec
}

// boundFiles returns the content of each regular file the configuration
// binds into the container, by its path in the container. Directories,
// devices and files too large to be configuration are left out: the
// guest's file system is the image's, and tabc carries no volumes.
func (b *Bundle) boundFiles() map[string]string {
	files := map[string]string{}

	for _, mount := range b.Spec.Mounts {
		if !isBind(mount) {
			continue
		}

		info, err := os.Stat(mount.Source)
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxBoundFile {
			continue
		}
		content, err := os.ReadFile(mount.Source)
		if err != nil {
			continue
		}
		files[plan.GuestPath(mount.Destination)] = string(content)
	}

	return files
}

// isBind reports whether a mount binds a host path, by its type or, for
// a mount with none, by its options.
func isBind(mount specs.Mount) bool {
	if mount.Type == mountTypeBind {
		return true
	}
	for _, option := range mount.Options {
		if option == optionBind || option == optionRbind {
			return true
		}
	}
	return false
}

// NetworkNamespace says which network the container is on. wanted is
// false when the configuration has no network namespace, which is a
// container on the host's network. Otherwise path names the namespace
// the engine made, or is "" when the runtime is asked to make one.
func (b *Bundle) NetworkNamespace() (path string, wanted bool) {
	if b.Spec.Linux == nil {
		return "", false
	}
	for _, namespace := range b.Spec.Linux.Namespaces {
		if namespace.Type == specs.NetworkNamespace {
			return strings.TrimSpace(namespace.Path), true
		}
	}
	return "", false
}

// ResolvConfPath returns the file of resolver configuration the engine
// binds at /etc/resolv.conf, or "" when the engine binds none. The file
// names servers that exist on the container's network, which is where
// the supervisor makes the guest's lookups.
func (b *Bundle) ResolvConfPath() string {
	for _, mount := range b.Spec.Mounts {
		if isBind(mount) && plan.GuestPath(mount.Destination) == resolvPath {
			return mount.Source
		}
	}
	return ""
}

// Plan returns the container as the supervisor runs it. An engine that
// asks for a terminal hands over a console socket for it.
func (b *Bundle) Plan() plan.Plan {
	terminal := plan.TerminalNone
	wanted, cols, rows := b.Terminal()
	if wanted {
		terminal = plan.TerminalConsole
	}

	return plan.Plan{
		Spec:           b.GuestSpec(NetworkOn),
		Terminal:       terminal,
		Cols:           int(cols),
		Rows:           int(rows),
		MemoryBytes:    b.MemoryLimit(),
		CPUs:           b.CPUs(),
		Volumes:        b.Volumes(),
		Rootfs:         b.Rootfs,
		ResolvConfPath: b.ResolvConfPath(),
	}
}

// Volumes returns the directories the configuration binds into the
// container: an engine's volumes and bind mounts, each a directory on
// the host. Single files are copied by content instead (boundFiles).
func (b *Bundle) Volumes() []plan.Volume {
	var volumes []plan.Volume
	for _, mount := range b.Spec.Mounts {
		if !isBind(mount) {
			continue
		}
		info, err := os.Stat(mount.Source)
		if err != nil || !info.IsDir() {
			continue
		}

		readOnly := false
		for _, option := range mount.Options {
			if option == optionReadOnly {
				readOnly = true
			}
		}
		volumes = append(volumes, plan.Volume{
			Source:   mount.Source,
			Target:   plan.GuestPath(mount.Destination),
			ReadOnly: readOnly,
		})
	}
	return volumes
}
