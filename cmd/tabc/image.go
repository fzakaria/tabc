package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/fzakaria/tabc/internal/image"
	"github.com/fzakaria/tabc/internal/plan"
	"github.com/fzakaria/tabc/internal/rootfs"
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

// envTerm is the variable that tells a program what its terminal is.
const envTerm = "TERM=xterm-256color"

// imageRun is a parsed `tabc run`.
type imageRun struct {
	interactive bool
	terminal    bool
	env         []string
	cwd         string
	user        string
	memory      int64
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
		case "-e", "--env":
			var entry string
			entry, err = value(name, inline, hasInline)
			parsed.env = append(parsed.env, entry)
		case "-w", "--workdir":
			parsed.cwd, err = value(name, inline, hasInline)
		case "-u", "--user":
			parsed.user, err = value(name, inline, hasInline)
		case "-m", "--memory":
			var text string
			text, err = value(name, inline, hasInline)
			if err == nil {
				parsed.memory, err = parseMemory(text)
			}
		default:
			err = fmt.Errorf("unknown flag %s", arg)
		}
		if err != nil {
			return parsed, err
		}
	}
	return parsed, errors.New("usage: tabc run [-it] [-e NAME=value] [-w dir] [-u user] [-m memory] <image> [program...]")
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
	pulled, err := image.Pull(context.Background(), parsed.image, cache, os.Stderr)
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
		Layers:      pulled.Layers,
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
	record := state.Container{ID: id, Status: state.Creating, Created: time.Now().UTC()}
	process, dir, err := createContainer(record, planned, launch{
		root:  root,
		id:    id,
		stdin: input,
		start: func(process *exec.Cmd) error { return process.Start() },
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
