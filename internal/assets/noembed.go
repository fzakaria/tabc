//go:build !embedshare

package assets

import "io/fs"

func embedded() fs.FS {
	return nil
}
