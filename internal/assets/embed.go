//go:build embedshare

package assets

import (
	"embed"
	"io/fs"
)

// share is filled in by the build (nix/package.nix) before the
// compiler runs.
//
//go:embed all:share
var share embed.FS

func embedded() fs.FS {
	files, err := fs.Sub(share, "share")
	if err != nil {
		return nil
	}
	return files
}
