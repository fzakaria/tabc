package plan

import (
	"slices"
	"testing"
)

// TestSplitMount checks that a mount is split at its colons into the
// source, the target and the options, by splitting mounts written the
// way every system writes one: with no drive letter.
func TestSplitMount(t *testing.T) {
	cases := map[string][]string{
		"/srv/data:/data":    {"/srv/data", "/data"},
		"/srv/data:/data:ro": {"/srv/data", "/data", "ro"},
		"cache:/var/cache":   {"cache", "/var/cache"},
		"/srv/data":          {"/srv/data"},
	}
	for text, want := range cases {
		if got := SplitMount(text); !slices.Equal(got, want) {
			t.Errorf("SplitMount(%q) = %q; want %q", text, got, want)
		}
	}
}

// TestGuestPath checks that a path in the guest is tidied as a Linux
// path on every host: its separator stays a slash.
func TestGuestPath(t *testing.T) {
	cases := map[string]string{
		"/data/":         "/data",
		"/etc/../data":   "/data",
		"/etc/hosts":     "/etc/hosts",
		"/var//cache/./": "/var/cache",
	}
	for text, want := range cases {
		if got := GuestPath(text); got != want {
			t.Errorf("GuestPath(%q) = %q; want %q", text, got, want)
		}
	}
}
