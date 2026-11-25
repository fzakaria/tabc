package state

import (
	"os"
	"testing"
	"time"
)

// TestSaveLoad checks that a record written for a live process reads
// back as written, and that the same record naming a process that does
// not exist reads as stopped, by saving both under a temporary root.
func TestSaveLoad(t *testing.T) {
	root := t.TempDir()
	dir, err := Create(root, "c1")
	if err != nil {
		t.Fatal(err)
	}

	// This test's own process stands in for a live supervisor.
	record := Container{
		ID:      "c1",
		Bundle:  "/bundle",
		Pid:     os.Getpid(),
		Status:  Running,
		Created: time.Now().UTC().Truncate(time.Second),
	}
	if err := Save(dir, record); err != nil {
		t.Fatal(err)
	}
	got, err := Load(root, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != Running || got.Bundle != "/bundle" || !got.Created.Equal(record.Created) {
		t.Errorf("got %+v, want %+v", got, record)
	}

	// No process has pid 0, so the supervisor counts as gone.
	record.Pid = 0
	if err := Save(dir, record); err != nil {
		t.Fatal(err)
	}
	got, err = Load(root, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != Stopped {
		t.Errorf("status %q for a dead supervisor, want %q", got.Status, Stopped)
	}
}

// TestCreate checks that an id can be taken once, and that ids which
// would escape the root are refused.
func TestCreate(t *testing.T) {
	root := t.TempDir()
	if _, err := Create(root, "c1"); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(root, "c1"); err == nil {
		t.Error("the same id was created twice")
	}
	for _, id := range []string{"", ".", "..", "a/b", "../x"} {
		if _, err := Create(root, id); err == nil {
			t.Errorf("id %q was accepted", id)
		}
	}
}

// TestLoadMissing checks that asking for a container that was never
// created is an error that says so.
func TestLoadMissing(t *testing.T) {
	if _, err := Load(t.TempDir(), "nope"); err == nil {
		t.Error("a missing container loaded")
	}
}
