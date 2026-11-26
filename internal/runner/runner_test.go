package runner

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSelfIsNotALink checks that the path tabc re-runs itself by names
// a file and no link, whatever name the process was started under. The
// binary is installed a second time as a link called tocker, and a
// supervisor or daemon started through that link would be tocker.
func TestSelfIsNotALink(t *testing.T) {
	self, err := Self()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(self)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Errorf("%s is a link", self)
	}
	if !filepath.IsAbs(self) {
		t.Errorf("%s is not absolute", self)
	}
}
