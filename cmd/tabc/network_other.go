//go:build !linux

package main

import (
	"os/exec"

	"github.com/fzakaria/tabc/internal/bundle"
)

// startInNetwork starts the supervisor. Network namespaces are Linux's,
// so anywhere else the container's network is the host's.
func startInNetwork(process *exec.Cmd, _ *bundle.Bundle) error {
	return process.Start()
}
