package main

import (
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/fzakaria/tabc/internal/cli"
	"github.com/fzakaria/tabc/internal/state"
)

// list prints the containers tabc knows of, as runc's list does:
//
//	tabc list
//
// No engine calls list. The command is for a person, who has no other
// way to learn the id `tabc run` gave a container, and whose `tabc exec`
// needs that id or the container's name.

// The table's columns, the space between them, and what stands for a
// container that has no name or no bundle.
const (
	listHeader  = "ID\tNAME\tPID\tSTATUS\tCREATED\tBUNDLE"
	listPadding = 2
	listNone    = "-"
)

// printList writes the table of containers under the invocation's root.
func printList(inv cli.Invocation, out io.Writer) error {
	containers, err := state.List(state.Root(inv.Root))
	if err != nil {
		return err
	}

	table := tabwriter.NewWriter(out, 0, 0, listPadding, ' ', 0)
	fmt.Fprintln(table, listHeader)
	for _, record := range containers {
		fmt.Fprintf(table, "%s\t%s\t%d\t%s\t%s\t%s\n",
			record.ID, orNone(record.Name), record.Pid, record.Status,
			record.Created.Format(time.RFC3339), orNone(record.Bundle))
	}
	return table.Flush()
}

// orNone returns text, or the table's mark for nothing.
func orNone(text string) string {
	if text == "" {
		return listNone
	}
	return text
}
