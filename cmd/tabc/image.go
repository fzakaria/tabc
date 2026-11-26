package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/fzakaria/tabc/internal/image"
	"github.com/fzakaria/tabc/internal/plan"
	"github.com/fzakaria/tabc/internal/rootfs"
	"github.com/fzakaria/tabc/internal/runner"
	"github.com/fzakaria/tabc/internal/state"
)

// `tabc run <image>` is tabc with no engine: tabc pulls the image, and
// the container's input and output are tabc's own. The flags are the
// ones `docker run` has for the same things.

// idBytes is the length of the random id a container gets here, where
// no engine names it.
const idBytes = 6

// The units a memory limit may be written in.
var memoryUnits = map[byte]int64{
	'k': 1 << 10,
	'm': 1 << 20,
	'g': 1 << 30,
}

// publishPartsWithIP is how many parts a published port has when it
// names an address, and portBits the width of a port number.
const (
	publishPartsWithIP = 3
	portBits           = 16
)

// envTerm is the variable that tells a program what its terminal is.
const envTerm = "TERM=xterm-256color"

// imageRun is a parsed `tabc run`.
type imageRun struct {
	interactive bool
	terminal    bool
	pull        bool
	open        bool
	link        bool
	env         []string
	cwd         string
	user        string
	memory      int64
	cpus        int
	publish     []plan.Publish
	volumes     []plan.Volume
	name        string
	networks    []string
	image       string
	command     []string
}

// parseImageRun reads the words after `run`: flags, then the image,
// then the program and its arguments, which are never read as flags.
func parseImageRun(args []string) (imageRun, error) {
	var parsed imageRun

	// value returns a flag's value: after "=", or the next word.
	index := 0
	value := func(flag, inline string, hasInline bool) (string, error) {
		if hasInline {
			return inline, nil
		}
		if index+1 >= len(args) {
			return "", fmt.Errorf("flag %s needs a value", flag)
		}
		index++
		return args[index], nil
	}

	for ; index < len(args); index++ {
		arg := args[index]
		if !strings.HasPrefix(arg, "-") {
			parsed.image = arg
			parsed.command = args[index+1:]
			return parsed, nil
		}

		name, inline, hasInline := strings.Cut(arg, "=")
		var err error
		switch name {
		case "-i", "--interactive":
			parsed.interactive = true
		case "-t", "--tty":
			parsed.terminal = true
		case "-it", "-ti":
			parsed.interactive = true
			parsed.terminal = true
		case "--rm":
			// Every container here is removed when its program ends.
		case "--open":
			// Run the container in the user's own browser, as a tab
			// they can see.
			parsed.open = true
		case "--link":
			// The same, started by the user opening the address tabc
			// prints.
			parsed.link = true
		case "--pull":
			// Ask the registry even for an image the cache has.
			parsed.pull = true
		case "-e", "--env":
			var entry string
			entry, err = value(name, inline, hasInline)
			parsed.env = append(parsed.env, entry)
		case "-w", "--workdir":
			parsed.cwd, err = value(name, inline, hasInline)
		case "-u", "--user":
			parsed.user, err = value(name, inline, hasInline)
		case "--name":
			parsed.name, err = value(name, inline, hasInline)
		case "--network":
			var network string
			network, err = value(name, inline, hasInline)
			parsed.networks = append(parsed.networks, network)
		case "-p", "--publish":
			var text string
			text, err = value(name, inline, hasInline)
			if err == nil {
				var published plan.Publish
				published, err = parsePublish(text)
				parsed.publish = append(parsed.publish, published)
			}
		case "-v", "--volume":
			var text string
			text, err = value(name, inline, hasInline)
			if err == nil {
				var volume plan.Volume
				volume, err = parseVolume(text)
				parsed.volumes = append(parsed.volumes, volume)
			}
		case "-m", "--memory":
			var text string
			text, err = value(name, inline, hasInline)
			if err == nil {
				parsed.memory, err = parseMemory(text)
			}
		case "--cpus":
			var text string
			text, err = value(name, inline, hasInline)
			if err == nil {
				parsed.cpus, err = parseCPUs(text)
			}
		default:
			err = fmt.Errorf("unknown flag %s", arg)
		}
		if err != nil {
			return parsed, err
		}
	}
	return parsed, errors.New("usage: tabc run [-it] [--open | --link] [--pull] [--name name] [--network network] [-p [address:]host:container] [-v directory:/path[:ro]] [-e NAME=value] [-w dir] [-u user] [-m memory] [--cpus count] <image> [program...]")
}

// parsePublish reads a published port as `docker run -p` writes one:
// "8080:80", "127.0.0.1:8080:80", or "80" for the same port on both
// sides. Without an address the port is published on loopback.
func parsePublish(text string) (plan.Publish, error) {
	var published plan.Publish
	parts := strings.Split(text, ":")

	// The address, when there are three parts.
	if len(parts) == publishPartsWithIP {
		published.HostIP = parts[0]
		parts = parts[1:]
	}
	if len(parts) == 0 || len(parts) > publishPartsWithIP-1 {
		return published, fmt.Errorf("published port %q is not [address:]host:container", text)
	}

	// One port is both; two are the host's, then the container's.
	numbers := make([]int, 0, len(parts))
	for _, part := range parts {
		number, err := strconv.ParseUint(part, 10, portBits)
		if err != nil || number == 0 {
			return published, fmt.Errorf("published port %q: %q is not a port", text, part)
		}
		numbers = append(numbers, int(number))
	}
	published.HostPort = numbers[0]
	published.GuestPort = numbers[len(numbers)-1]
	return published, nil
}

// parseVolume reads a mount as `docker run -v` writes one:
// "directory:path" or "directory:path:ro". The directory is the
// host's, and is made absolute, since the supervisor does not stand
// where the user does.
func parseVolume(text string) (plan.Volume, error) {
	parts := strings.Split(text, ":")
	if len(parts) < 2 || len(parts) > 3 || parts[0] == "" || !strings.HasPrefix(parts[1], "/") {
		return plan.Volume{}, fmt.Errorf("volume %q is not directory:/path[:ro]", text)
	}
	source, err := filepath.Abs(parts[0])
	if err != nil {
		return plan.Volume{}, err
	}
	info, err := os.Stat(source)
	if err != nil {
		return plan.Volume{}, fmt.Errorf("volume %s: %w", parts[0], err)
	}
	if !info.IsDir() {
		return plan.Volume{}, fmt.Errorf("volume %s is not a directory", parts[0])
	}
	return plan.Volume{
		Source:   source,
		Target:   filepath.Clean(parts[1]),
		ReadOnly: len(parts) == 3 && parts[2] == "ro",
	}, nil
}

// parseMemory reads a memory limit: a number of bytes, or a number with
// k, m or g after it.
func parseMemory(text string) (int64, error) {
	if text == "" {
		return 0, errors.New("an empty memory limit")
	}

	unit := int64(1)
	digits := text
	if scale, has := memoryUnits[strings.ToLower(text)[len(text)-1]]; has {
		unit = scale
		digits = text[:len(text)-1]
	}
	amount, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || amount <= 0 {
		return 0, fmt.Errorf("memory limit %q is not a size", text)
	}
	return amount * unit, nil
}

// parseCPUs reads a count of CPUs as `docker run --cpus` takes one, a
// number that may have a fraction, and returns the whole number of
// CPUs that holds the count.
func parseCPUs(text string) (int, error) {
	count, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(count) || math.IsInf(count, 0) || count <= 0 {
		return 0, fmt.Errorf("CPU count %q is not a positive number", text)
	}
	return int(math.Ceil(count)), nil
}

// runImage pulls an image and runs a program from it, and returns the
// program's status.
func runImage(args []string) (int, error) {
	parsed, err := parseImageRun(args)
	if err != nil {
		return 0, err
	}

	// The image, from the cache or the registry.
	cache, err := image.CacheDir()
	if err != nil {
		return 0, err
	}
	refresh := image.UseCached
	if parsed.pull {
		refresh = image.AlwaysPull
	}
	pulled, err := image.Get(context.Background(), parsed.image, cache, refresh, os.Stderr)
	if err != nil {
		return 0, err
	}

	// What the guest is told, with the image's own files open for
	// looking up a user given by name.
	tree, err := rootfs.OpenLayers(pulled.Layers)
	if err != nil {
		return 0, err
	}
	overrides := image.Overrides{Command: parsed.command, Env: parsed.env, Cwd: parsed.cwd, User: parsed.user}
	if parsed.terminal {
		overrides.Env = append([]string{envTerm}, overrides.Env...)
	}
	spec, err := image.Spec(pulled.Config, overrides, tree)
	tree.Close()
	if err != nil {
		return 0, err
	}

	planned := plan.Plan{
		Spec:        spec,
		Terminal:    plan.TerminalNone,
		MemoryBytes: parsed.memory,
		CPUs:        parsed.cpus,
		Layers:      pulled.Layers,
		Publish:     parsed.publish,
		Volumes:     parsed.volumes,
		Title:       strings.Join(append([]string{parsed.image}, parsed.command...), " "),
	}
	if parsed.open {
		planned.Browser = plan.BrowserOpen
	}
	if parsed.link {
		planned.Browser = plan.BrowserLink
	}

	// A terminal is the one tabc was started on, raw for as long as the
	// program runs: the guest's terminal does the line editing and the
	// echo, and every key has to reach the guest as typed.
	stdin := int(os.Stdin.Fd())
	if parsed.terminal {
		if !term.IsTerminal(stdin) {
			return 0, errors.New("-t needs a terminal, and the input is not one")
		}
		planned.Terminal = plan.TerminalInherit
		planned.Cols, planned.Rows, _ = term.GetSize(stdin)

		saved, err := term.MakeRaw(stdin)
		if err != nil {
			return 0, err
		}
		defer term.Restore(stdin, saved)
	}

	// Without -i the program has no input, as with every engine.
	var input *os.File
	if !parsed.interactive && !parsed.terminal {
		input, err = os.Open(os.DevNull)
		if err != nil {
			return 0, err
		}
		defer input.Close()
	}

	id, err := newID()
	if err != nil {
		return 0, err
	}
	root := state.Root("")

	// On each network the container answers to its id and, if it was
	// given one, its name.
	names := []string{id}
	if parsed.name != "" {
		names = append(names, parsed.name)
	}
	for _, network := range parsed.networks {
		planned.Networks = append(planned.Networks, plan.Network{Name: network, Names: names})
	}
	record := state.Container{ID: id, Name: parsed.name, Status: state.Creating, Created: time.Now().UTC()}
	process, dir, err := runner.Create(record, planned, runner.Launch{
		Root:  root,
		ID:    id,
		Stdin: input,
		Start: func(process *exec.Cmd) error { return process.Start() },
	})
	if err != nil {
		return 0, err
	}
	return startAndWait(process, root, id, dir)
}

// newID returns a random container id.
func newID() (string, error) {
	random := make([]byte, idBytes)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return hex.EncodeToString(random), nil
}
