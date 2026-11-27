package main

import (
	"fmt"
	"io"
	"path/filepath"

	"github.com/fzakaria/tabc/internal/cli"
	"github.com/fzakaria/tabc/internal/state"
	"github.com/fzakaria/tabc/internal/supervisor"
)

// park moves a running container out of its browser tab:
//
//	tabc park <id or name>
//
// The container's page saves the guest, the program in it stopped
// mid-step, and hands the snapshot to the supervisor. The tab can then
// be closed. The next browser to open the container's address resumes
// the guest, and the program carries on in the same process with what
// it had in memory. Only a container started with `tabc run --movable`
// can be parked.

// The names of the numbers a page reports for a park (page/guest.js),
// and the units they are printed in.
const (
	statSnapshotBytes = "bytes"
	statStoredBytes   = "storedBytes"
	statSnapshotMs    = "snapshotMs"
	statEncodeMs      = "encodeMs"
	statCompressMs    = "compressMs"
	statStoreMs       = "storeMs"
	bytesPerMiB       = 1 << 20
)

// park parks the container the invocation names and writes what
// happened, and where the container resumes, to out.
func park(inv cli.Invocation, out io.Writer) error {
	reference, err := inv.ID()
	if err != nil {
		return err
	}
	root := state.Root(inv.Root)

	// The container by its id, or by the name `tabc run` gave it.
	record, err := state.Find(root, reference)
	if err != nil {
		return err
	}
	if record.Status != state.Running {
		return fmt.Errorf("container %q is %s, not running", record.ID, record.Status)
	}

	result, err := supervisor.Park(filepath.Join(state.Dir(root, record.ID), state.SocketFile))
	if err != nil {
		return err
	}

	stats := result.Stats
	fmt.Fprintf(out, "parked %s: a snapshot of %.1f MiB, %.1f MiB as stored (guest saved in %.0f ms, encoded in %.0f ms, compressed in %.0f ms, stored in %.0f ms)\n",
		record.ID, stats[statSnapshotBytes]/bytesPerMiB, stats[statStoredBytes]/bytesPerMiB,
		stats[statSnapshotMs], stats[statEncodeMs], stats[statCompressMs], stats[statStoreMs])
	if record.URL != "" {
		fmt.Fprintf(out, "open this page to resume the container:\n  %s\n", record.URL)
	}
	return nil
}
