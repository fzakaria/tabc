// Package assets is everything the supervisor serves to a container's
// page: the page itself, the unkernel SDK with its kernel, and the
// guest's init, laid out as nix/share.nix builds them.
//
// A released tabc carries all of it inside the binary, so installing
// tabc is copying one file. A plain `go build` carries none of it,
// since the kernel is not in this repository, and reads a directory
// named by TABC_SHARE instead.
package assets

import "io/fs"

// The layout: a directory for the page, one for the SDK, one for the
// page's terminal, and init.
const (
	Page     = "page"
	Unkernel = "unkernel"
	Xterm    = "xterm"
	Init     = "init"
)

// Embedded returns the files built into this binary, or nil when the
// binary was built without them.
func Embedded() fs.FS {
	return embedded()
}
