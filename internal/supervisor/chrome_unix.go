//go:build !windows

package supervisor

import (
	"os/exec"
	"syscall"
)

// runAsNobody makes the browser a command starts run as the user and
// group nobody.
func runAsNobody(browser *exec.Cmd) {
	browser.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{Uid: unprivilegedID, Gid: unprivilegedID},
	}
}
