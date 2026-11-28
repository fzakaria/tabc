//go:build !windows

package rootfs

// shownMode returns the mode a file reads with after it was given
// mode: the host keeps permission bits, so the same one.
func shownMode(mode uint32) uint32 {
	return mode
}
