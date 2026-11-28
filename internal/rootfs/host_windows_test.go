package rootfs

// shownMode returns the mode a file reads with after it was given
// mode: Windows keeps no permission bits, so the mode every writable
// file is shown with.
func shownMode(uint32) uint32 {
	return modeWritable
}
