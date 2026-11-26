//go:build linux

package main

import (
	"reflect"
	"testing"
)

// TestListeningPorts checks which sockets count as a container's open
// ports: listening ones on every address or on the machine's own,
// while a socket listening on loopback alone, a connected socket and a
// malformed line do not. The text is /proc/net/tcp as the kernel prints
// it, addresses in the machine's byte order.
func TestListeningPorts(t *testing.T) {
	table := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:0050 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1 1 0 100 0 0 10 0
   1: 0100007F:18EB 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 2 1 0 100 0 0 10 0
   2: 0F02000A:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 3 1 0 100 0 0 10 0
   3: 0F02000A:C350 0A00020F:0050 01 00000000:00000000 00:00000000 00000000     0        0 4 1 0 100 0 0 10 0
   4: garbage
`
	got := listeningPorts(table)
	want := map[int]bool{80: true, 8080: true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestPortChanges checks the lines the watcher prints when the set of
// open ports changes: one per port opened and one per port closed, in
// port order, and nothing when nothing changed.
func TestPortChanges(t *testing.T) {
	before := map[int]bool{80: true, 5432: true}
	after := map[int]bool{80: true, 6379: true, 443: true}

	got := portChanges(before, after)
	want := []string{"+443", "-5432", "+6379"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if changes := portChanges(after, after); len(changes) != 0 {
		t.Errorf("no change printed %v", changes)
	}
}
