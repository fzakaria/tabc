package state

import (
	"os"
	"reflect"
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
	for _, id := range []string{"", ".", "..", "a/b", "../x", ".networks"} {
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

// TestFindAndList checks that a container is found by its id or by the
// name it was given, that a name two containers share finds neither,
// and that the list has every container, oldest first. Three records
// are saved under a temporary root.
func TestFindAndList(t *testing.T) {
	root := t.TempDir()
	started := time.Now().UTC().Truncate(time.Second)
	records := []Container{
		{ID: "b2", Name: "web", Created: started.Add(time.Second)},
		{ID: "a1", Name: "twin", Created: started},
		{ID: "c3", Name: "twin", Created: started.Add(2 * time.Second)},
	}
	for _, record := range records {
		dir, err := Create(root, record.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := Save(dir, record); err != nil {
			t.Fatal(err)
		}
	}

	// What the root holds besides containers is not one.
	if err := os.Mkdir(root+"/.networks", 0o700); err != nil {
		t.Fatal(err)
	}

	for reference, id := range map[string]string{"b2": "b2", "web": "b2", "a1": "a1"} {
		found, err := Find(root, reference)
		if err != nil || found.ID != id {
			t.Errorf("Find(%q) = %q, %v; want %q", reference, found.ID, err, id)
		}
	}
	for _, reference := range []string{"twin", "nothing"} {
		if found, err := Find(root, reference); err == nil {
			t.Errorf("Find(%q) found %q", reference, found.ID)
		}
	}

	listed, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, record := range listed {
		ids = append(ids, record.ID)
	}
	if want := []string{"a1", "b2", "c3"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("listed %v, want %v", ids, want)
	}

	// A root that was never made has no containers, which is no error.
	if listed, err := List(root + "/absent"); err != nil || len(listed) != 0 {
		t.Errorf("an absent root listed %v, %v", listed, err)
	}
}
