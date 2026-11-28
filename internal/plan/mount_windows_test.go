package plan

import (
	"slices"
	"testing"
)

// TestSplitMountWithDrive checks that the colon after a Windows drive
// letter stays with the source, by splitting mounts whose source names
// a drive with either separator, and one whose source is on a share.
func TestSplitMountWithDrive(t *testing.T) {
	cases := map[string][]string{
		`C:\Users\me\data:/data`:    {`C:\Users\me\data`, "/data"},
		`C:\Users\me\data:/data:ro`: {`C:\Users\me\data`, "/data", "ro"},
		`C:/Users/me/data:/data`:    {`C:/Users/me/data`, "/data"},
		`\\host\share\data:/data`:   {`\\host\share\data`, "/data"},
		`cache:/var/cache`:          {"cache", "/var/cache"},
	}
	for text, want := range cases {
		if got := SplitMount(text); !slices.Equal(got, want) {
			t.Errorf("SplitMount(%q) = %q; want %q", text, got, want)
		}
	}
}
