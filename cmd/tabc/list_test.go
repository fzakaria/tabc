package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/fzakaria/tabc/internal/cli"
	"github.com/fzakaria/tabc/internal/state"
)

// TestPrintList checks the table `tabc list` prints: a header, then a
// line per container with its id, its name or a dash, and its status.
// Two records are saved under a temporary root; neither has a live
// supervisor, so both read as stopped.
func TestPrintList(t *testing.T) {
	root := t.TempDir()
	created := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	for index, record := range []state.Container{
		{ID: "aaa111", Name: "web", Status: state.Running},
		{ID: "bbb222", Bundle: "/bundles/b", Status: state.Running},
	} {
		record.Created = created.Add(time.Duration(index) * time.Second)
		dir, err := state.Create(root, record.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := state.Save(dir, record); err != nil {
			t.Fatal(err)
		}
	}

	var out bytes.Buffer
	if err := printList(cli.Invocation{Root: root}, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	want := [][]string{
		{"ID", "NAME", "PID", "STATUS", "CREATED", "BUNDLE"},
		{"aaa111", "web", "0", "stopped", "2026-11-26T09:00:00Z", "-"},
		{"bbb222", "-", "0", "stopped", "2026-11-26T09:00:01Z", "/bundles/b"},
	}
	if len(lines) != len(want) {
		t.Fatalf("%d lines, want %d:\n%s", len(lines), len(want), out.String())
	}
	for index, line := range lines {
		if got := strings.Fields(line); strings.Join(got, " ") != strings.Join(want[index], " ") {
			t.Errorf("line %d: %q, want %q", index, got, want[index])
		}
	}
}
