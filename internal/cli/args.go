// Package cli reads the command line container engines give an OCI
// runtime. The shape is runc's, since containerd's shim and podman's
// conmon both drive a runtime the way they drive runc:
//
//	tabc [global flags] <command> [command flags] <container id> [arguments]
package cli

import (
	"fmt"
	"strings"
)

// The flags tabc acts on.
const (
	flagRoot         = "root"
	flagLog          = "log"
	flagLogFormat    = "log-format"
	flagBundle       = "bundle"
	flagBundleShort  = "b"
	flagPidFile      = "pid-file"
	flagForce        = "force"
	flagForceShort   = "f"
	flagAll          = "all"
	flagAllShort     = "a"
	flagVersion      = "version"
	flagVersionShort = "v"
	// FlagConsoleSocket names the socket an engine waits on for the
	// program's terminal.
	FlagConsoleSocket = "console-socket"
	// FlagMirrorPorts is create's word to the supervisor that the
	// supervisor runs in the container's own network namespace.
	FlagMirrorPorts = "mirror-ports"
)

// valueFlags are the flags that take a value, the ones tabc reads and
// the ones engines pass that tabc has no use for. Every other flag is a
// switch. Knowing which is which is what lets an unknown switch be
// skipped without mistaking the word after it for the command.
var valueFlags = map[string]bool{
	flagRoot:          true,
	flagLog:           true,
	flagLogFormat:     true,
	flagBundle:        true,
	flagBundleShort:   true,
	flagPidFile:       true,
	FlagConsoleSocket: true,
	"criu":            true,
	"rootless":        true,
	"preserve-fds":    true,
	"cgroup-manager":  true,
	"format":          true,
}

// Invocation is one call of the runtime.
type Invocation struct {
	// Root is the directory container state is kept under, empty for the
	// default.
	Root string

	// Log and LogFormat are where an engine wants errors written, and
	// how.
	Log       string
	LogFormat string

	// Command is the lifecycle command, empty when none was given.
	Command string

	// Bundle, PidFile and ConsoleSocket are `create`'s flags.
	Bundle        string
	PidFile       string
	ConsoleSocket string

	// Force is `delete --force`, All is `kill --all`, Version is
	// `--version` given in place of a command.
	Force   bool
	All     bool
	Version bool

	// MirrorPorts is the supervisor's own flag (FlagMirrorPorts).
	MirrorPorts bool

	// Args are the words after the command that are not flags: the
	// container's id first.
	Args []string
}

// Parse reads the arguments after the program's name.
func Parse(args []string) (Invocation, error) {
	var inv Invocation

	for i := 0; i < len(args); i++ {
		arg := args[i]

		// A word that is not a flag is the command, or after the command
		// one of its arguments.
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			if inv.Command == "" {
				inv.Command = arg
				continue
			}
			inv.Args = append(inv.Args, arg)
			continue
		}

		// A flag is "--name", "-n", "--name=value" or "--name value".
		name, value, inline := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if valueFlags[name] && !inline {
			if i+1 >= len(args) {
				return inv, fmt.Errorf("flag %s needs a value", arg)
			}
			i++
			value = args[i]
		}

		switch name {
		case flagRoot:
			inv.Root = value
		case flagLog:
			inv.Log = value
		case flagLogFormat:
			inv.LogFormat = value
		case flagBundle, flagBundleShort:
			inv.Bundle = value
		case flagPidFile:
			inv.PidFile = value
		case FlagConsoleSocket:
			inv.ConsoleSocket = value
		case flagForce, flagForceShort:
			inv.Force = true
		case flagAll, flagAllShort:
			inv.All = true
		case flagVersion, flagVersionShort:
			inv.Version = true
		case FlagMirrorPorts:
			inv.MirrorPorts = true
		}
	}

	return inv, nil
}

// ID returns the container id, the first argument after the command.
func (inv Invocation) ID() (string, error) {
	if len(inv.Args) == 0 {
		return "", fmt.Errorf("%s: a container id is required", inv.Command)
	}
	return inv.Args[0], nil
}
