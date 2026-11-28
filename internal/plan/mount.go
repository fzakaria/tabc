package plan

import (
	"path"
	"path/filepath"
	"strings"
)

// mountSeparator stands between a mount's source, its target and its
// options, as `docker run -v` writes a mount.
const mountSeparator = ":"

// SplitMount splits a mount written source:target[:options] into its
// parts. A Windows source starts with a drive letter and a colon, or
// with a share's name, and that part stays with the source. No other
// system's paths have such a part, so there the split is at every
// colon.
func SplitMount(text string) []string {
	drive := filepath.VolumeName(text)
	parts := strings.Split(text[len(drive):], mountSeparator)
	parts[0] = drive + parts[0]
	return parts
}

// GuestPath tidies a path in the guest. The guest is Linux on every
// host, so the path is tidied as a Linux path and not as the host's:
// on Windows the host's way would turn every slash around.
func GuestPath(text string) string {
	return path.Clean(text)
}
