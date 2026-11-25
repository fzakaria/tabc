// Package state keeps what tabc knows about each container between the
// short-lived commands an engine runs: a directory per container under
// the runtime's root, holding the state file, the supervisor's control
// socket and its log.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Status is where a container is in its life, in the words of the OCI
// runtime specification.
type Status string

const (
	// Creating: the supervisor is starting the browser and the guest.
	Creating Status = "creating"
	// Created: the guest is up and waits for `start`.
	Created Status = "created"
	// Running: the container's program runs.
	Running Status = "running"
	// Stopped: the program ended, or the supervisor is gone.
	Stopped Status = "stopped"
)

// The files in a container's directory.
const (
	stateFile = "state.json"
	// SocketFile is the supervisor's control socket.
	SocketFile = "ctl.sock"
	// LogFile is the supervisor's log.
	LogFile = "supervisor.log"
)

// defaultRoot is the root for the superuser, and rootDirName the
// directory under the user's runtime directory for anyone else.
const (
	systemRunDir = "/run"
	defaultRoot  = systemRunDir + "/tabc"
	rootDirName  = "tabc"
)

// accessWrite asks access(2) whether a path may be written (W_OK).
const accessWrite = 2

// The permissions of a container's directory and of its state file.
const (
	dirMode  = 0o711
	fileMode = 0o644
)

// Container is one container's record.
type Container struct {
	ID          string            `json:"id"`
	Bundle      string            `json:"bundle"`
	Pid         int               `json:"pid"`
	Status      Status            `json:"status"`
	Created     time.Time         `json:"created"`
	Annotations map[string]string `json:"annotations,omitempty"`

	// Profile is the browser's profile directory, which may be outside
	// the container's directory (internal/supervisor, makeProfile).
	Profile string `json:"profile,omitempty"`
}

// Root returns the directory state is kept under: the one given, else
// /run/tabc for the superuser and a directory under XDG_RUNTIME_DIR, or
// the temporary directory, for anyone else.
//
// A rootless engine runs the runtime as the superuser of a user
// namespace, who cannot write to /run. That superuser is anyone else:
// what decides is whether /run can be written, not the user's number.
func Root(given string) string {
	if given != "" {
		return given
	}
	if os.Geteuid() == 0 && syscall.Access(systemRunDir, accessWrite) == nil {
		return defaultRoot
	}
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		return filepath.Join(runtimeDir, rootDirName)
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("%s-%d", rootDirName, os.Geteuid()))
}

// Dir returns a container's directory under root.
func Dir(root, id string) string {
	return filepath.Join(root, id)
}

// Create makes a container's directory, and fails if the id is taken.
func Create(root, id string) (string, error) {
	if id == "" || id != filepath.Base(id) || id == "." || id == ".." {
		return "", fmt.Errorf("invalid container id %q", id)
	}
	if err := os.MkdirAll(root, dirMode); err != nil {
		return "", err
	}

	dir := Dir(root, id)
	if err := os.Mkdir(dir, dirMode); err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("container %q already exists", id)
		}
		return "", err
	}
	return dir, nil
}

// Save writes a container's record, replacing the file in one step so a
// reader never sees half of one.
func Save(dir string, c Container) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}

	temporary := filepath.Join(dir, stateFile+".tmp")
	if err := os.WriteFile(temporary, data, fileMode); err != nil {
		return err
	}
	return os.Rename(temporary, filepath.Join(dir, stateFile))
}

// Load reads a container's record. A container whose supervisor is gone
// reads as stopped whatever the file says, since a supervisor that was
// killed could not write its last status.
func Load(root, id string) (Container, error) {
	var c Container

	data, err := os.ReadFile(filepath.Join(Dir(root, id), stateFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return c, fmt.Errorf("container %q does not exist", id)
		}
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("container %q: %w", id, err)
	}

	if c.Status != Stopped && !alive(c.Pid) {
		c.Status = Stopped
	}
	return c, nil
}

// Remove deletes a container's directory, and the browser's profile if
// the record names one elsewhere.
func Remove(root, id string) error {
	if record, err := Load(root, id); err == nil && record.Profile != "" {
		os.RemoveAll(record.Profile)
	}
	return os.RemoveAll(Dir(root, id))
}

// alive reports whether a process exists. Signal 0 checks for the
// process without sending anything; EPERM means the process exists and
// belongs to someone else.
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
