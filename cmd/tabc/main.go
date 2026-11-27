// tabc is an OCI runtime that runs a container in a browser tab.
//
// An engine drives it the way engines drive runc:
//
//	tabc create --bundle <dir> --pid-file <file> <id>
//	tabc start <id>
//	tabc state <id>
//	tabc exec <id> <program> [arguments]
//	tabc kill <id> <signal>
//	tabc delete <id>
//
// and with no engine at all, tabc pulls an image and runs it:
//
//	tabc run [-it] <image> [program and arguments]
//
// or stands in for the engine, so the Docker command line and Compose
// work where there is no Docker (daemon.go, tocker.go):
//
//	tocker compose up
//
// create starts a supervisor (internal/supervisor), which starts
// headless Chromium on a page that boots a Linux guest with the
// container's image as its root. start runs the container's program in
// that guest.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	specs "github.com/opencontainers/runtime-spec/specs-go"

	"github.com/fzakaria/tabc/internal/cli"
	"github.com/fzakaria/tabc/internal/runner"
	"github.com/fzakaria/tabc/internal/state"
	"github.com/fzakaria/tabc/internal/supervisor"
)

// version is tabc's own version, set when the package is built.
var version = "dev"

// The commands.
const (
	commandCreate   = "create"
	commandStart    = "start"
	commandState    = "state"
	commandKill     = "kill"
	commandDelete   = "delete"
	commandFeatures = "features"
	commandList     = "list"
	commandPull     = "pull"
)

// exitError is tabc's own failure status, apart from a program's.
const exitError = 1

// logFormatJSON is the log format engines ask for, one JSON object a
// line, which is how containerd's shim reads a runtime's error back.
const logFormatJSON = "json"

func main() {
	// Run under tocker's name, or as `tabc tocker`, the words that
	// follow are a docker command and not an engine's.
	if isTocker() {
		fatal(cli.Invocation{}, tocker(os.Args[1:]))
	}
	if len(os.Args) > 1 && os.Args[1] == commandTocker {
		fatal(cli.Invocation{}, tocker(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == commandDaemon {
		if err := daemon(os.Args[2:]); err != nil {
			fatal(cli.Invocation{}, err)
		}
		return
	}

	inv, err := cli.Parse(os.Args[1:])
	if err != nil {
		fatal(inv, err)
	}

	if inv.Version {
		fmt.Printf("tabc version %s\nspec: %s\n", version, specs.Version)
		return
	}

	switch inv.Command {
	case commandCreate:
		err = create(inv)
	case commandStart:
		err = start(inv)
	case commandState:
		err = printState(inv)
	case commandKill:
		err = kill(inv)
	case commandDelete:
		err = remove(inv)
	case commandFeatures:
		err = printFeatures()
	case commandList:
		err = printList(inv, os.Stdout)
	case cli.CommandRun:
		// With a bundle, run is the lifecycle in one command. Without
		// one, the words after run name an image and a program.
		var status int
		if inv.Bundle != "" {
			status, err = runBundle(inv)
		} else {
			status, err = runImage(argumentsAfter(os.Args[1:], cli.CommandRun))
		}
		if err == nil {
			os.Exit(status)
		}
	case commandPull:
		err = pullImage(argumentsAfter(os.Args[1:], commandPull))
	case commandExec:
		var status int
		status, err = execCommand(inv, argumentsAfter(os.Args[1:], commandExec))
		if err == nil {
			os.Exit(status)
		}
	case runner.CommandSupervise:
		os.Exit(supervise(inv))
	case "":
		err = errors.New("no command given; see https://github.com/fzakaria/tabc")
	default:
		err = fmt.Errorf("unknown command %q", inv.Command)
	}

	if err != nil {
		fatal(inv, err)
	}
}

// fatal reports an error where the engine asked for errors, and on
// stderr, and exits.
func fatal(inv cli.Invocation, err error) {
	if err == nil {
		os.Exit(0)
	}
	if inv.Log != "" {
		writeLog(inv.Log, inv.LogFormat, err)
	}
	fmt.Fprintf(os.Stderr, "tabc: %v\n", err)
	os.Exit(exitError)
}

// writeLog appends an error to the engine's log file in the format the
// engine named.
func writeLog(path, format string, err error) {
	file, openErr := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if openErr != nil {
		return
	}
	defer file.Close()

	if format != logFormatJSON {
		fmt.Fprintf(file, "%s\n", err)
		return
	}
	json.NewEncoder(file).Encode(map[string]string{
		"level": "error",
		"msg":   err.Error(),
		"time":  time.Now().Format(time.RFC3339),
	})
}

// start runs the program of a created container.
func start(inv cli.Invocation) error {
	id, err := inv.ID()
	if err != nil {
		return err
	}
	root := state.Root(inv.Root)

	record, err := state.Load(root, id)
	if err != nil {
		return err
	}
	if record.Status != state.Created {
		return fmt.Errorf("container %q is %s, not created", id, record.Status)
	}
	return supervisor.Start(filepath.Join(state.Dir(root, id), state.SocketFile))
}

// ociState is a container's state as the runtime specification has an
// engine read it.
type ociState struct {
	Version     string            `json:"ociVersion"`
	ID          string            `json:"id"`
	Status      state.Status      `json:"status"`
	Pid         int               `json:"pid,omitempty"`
	Bundle      string            `json:"bundle"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// stateOf returns a record in the specification's shape.
func stateOf(record state.Container) ociState {
	return ociState{
		Version:     specs.Version,
		ID:          record.ID,
		Status:      record.Status,
		Pid:         record.Pid,
		Bundle:      record.Bundle,
		Annotations: record.Annotations,
	}
}

// printState writes a container's state to stdout.
func printState(inv cli.Invocation) error {
	id, err := inv.ID()
	if err != nil {
		return err
	}
	record, err := state.Load(state.Root(inv.Root), id)
	if err != nil {
		return err
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(stateOf(record))
}

// printFeatures writes what an engine may ask a runtime about itself:
// the versions of the specification tabc reads.
func printFeatures() error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(map[string]string{
		"ociVersionMin": "1.0.0",
		"ociVersionMax": specs.Version,
	})
}

// argumentsAfter returns the words after the first `command` in args.
func argumentsAfter(args []string, command string) []string {
	for index, arg := range args {
		if arg == command {
			return args[index+1:]
		}
	}
	return nil
}
