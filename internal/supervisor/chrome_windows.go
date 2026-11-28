package supervisor

import "os/exec"

// runAsNobody has nothing to do on Windows, which has no superuser for
// the browser to give up: the browser runs as the user tabc runs as.
func runAsNobody(*exec.Cmd) {}
