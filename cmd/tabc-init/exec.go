//go:build linux

package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/fzakaria/tabc/internal/guest"
	"github.com/fzakaria/tabc/internal/users"
)

// A second program in a container that is already running: what
// `tabc exec` asks for. The first init built the container's root and
// left the root mounted where every process of the guest can see it, so
// a second program needs only to enter that root. The guest has one
// container, so there is no namespace to join.

// readyName is the file the first init leaves in the scratch directory
// once the root is whole: mounted, with the engine's files written and
// the volumes bound.
const readyName = "ready"

// readyWait is how long a second program waits for the root, which is
// only ever not there in the moment after the container's own program
// was started, and readyPoll how often the second program looks.
const (
	readyWait = 10 * time.Second
	readyPoll = 10 * time.Millisecond
)

// markReady leaves the file that says the container's root is whole.
func markReady() error {
	return os.WriteFile(filepath.Join(scratchDir, readyName), nil, fileMode)
}

// execInContainer runs a second program in the container's root and
// does not return. encoded is a guest.Spec as JSON in base64: the
// program, its environment, its directory and its user.
func execInContainer(encoded string) {
	spec, err := decodeSpec(encoded)
	if err != nil {
		fail(exitSetupFailed, err)
	}
	if err := awaitReady(); err != nil {
		fail(exitSetupFailed, err)
	}

	// The same root the container's own program is in.
	if err := syscall.Chroot(filepath.Join(scratchDir, mergedName)); err != nil {
		fail(exitSetupFailed, fmt.Errorf("entering the root: %w", err))
	}
	if err := os.Chdir(spec.Cwd); err != nil {
		fail(exitSetupFailed, err)
	}

	// A user by name is looked up now, in the root: the passwd file is
	// the running container's, with whoever was added since the image.
	if spec.User != "" {
		spec.UID, spec.GID, err = users.Resolve(spec.User, os.ReadFile)
		if err != nil {
			fail(exitSetupFailed, err)
		}
		spec.AdditionalGids = nil
	}
	runProgram(spec)
}

// decodeSpec reads a spec handed over as one argument.
func decodeSpec(encoded string) (guest.Spec, error) {
	var spec guest.Spec
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return spec, fmt.Errorf("the exec spec: %w", err)
	}
	if err := json.Unmarshal(data, &spec); err != nil {
		return spec, fmt.Errorf("the exec spec: %w", err)
	}
	if len(spec.Args) == 0 {
		return spec, errors.New("the exec spec names no program")
	}
	if spec.Cwd == "" {
		spec.Cwd = defaultHome
	}
	return spec, nil
}

// awaitReady waits until the first init has left the root whole.
func awaitReady() error {
	marker := filepath.Join(scratchDir, readyName)
	deadline := time.Now().Add(readyWait)
	for {
		if _, err := os.Stat(marker); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("the container has no root yet")
		}
		time.Sleep(readyPoll)
	}
}
