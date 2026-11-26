//go:build linux

package main

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The port watcher: `tabc-init --ports`. The page runs it beside the
// container's program, and it prints a line whenever the program
// starts or stops listening on a TCP port: "+80", "-80". The
// supervisor listens on the same port at the container's address and
// carries each connection into the guest, which is how anything
// outside a container reaches a server inside one.
//
// The guest is one machine with one network, so the kernel's table of
// sockets is the container's, read from outside the container's root.

// tcpTable is where the kernel lists TCP sockets, one a line, and
// pollInterval how often the watcher reads it. A server is reachable at
// most one interval after it starts listening.
const (
	tcpTable     = "/proc/net/tcp"
	pollInterval = 250 * time.Millisecond
)

// A socket's line in the table: the position of its local address and
// of its state, how many fields a line has at least, and the state of a
// socket that listens.
const (
	fieldLocal     = 1
	fieldState     = 3
	minimumFields  = 4
	stateListening = "0A"
)

// loopbackSuffix ends the local address of a socket bound to loopback:
// the table prints an address as one number in the machine's byte
// order, so 127.x.y.z ends in 7F. A server that listens on loopback
// alone has said the outside is not to reach it.
const loopbackSuffix = "7F"

// The signs a change is printed with, and the base ports are written in
// in the table.
const (
	signOpened = "+"
	signClosed = "-"
	hexBase    = 16
	portBits   = 16
)

// watchPorts prints the container's open ports as they change, forever.
func watchPorts() {
	open := map[int]bool{}
	for {
		table, err := os.ReadFile(tcpTable)
		if err == nil {
			now := listeningPorts(string(table))
			for _, change := range portChanges(open, now) {
				fmt.Println(change)
			}
			open = now
		}
		time.Sleep(pollInterval)
	}
}

// listeningPorts returns the ports that sockets in the kernel's table
// listen on for connections from outside the machine.
func listeningPorts(table string) map[int]bool {
	ports := map[int]bool{}
	for _, line := range strings.Split(table, "\n") {
		fields := strings.Fields(line)
		if len(fields) < minimumFields || fields[fieldState] != stateListening {
			continue
		}

		address, portText, found := strings.Cut(fields[fieldLocal], ":")
		if !found || strings.HasSuffix(address, loopbackSuffix) {
			continue
		}
		port, err := strconv.ParseUint(portText, hexBase, portBits)
		if err != nil {
			continue
		}
		ports[int(port)] = true
	}
	return ports
}

// portChanges returns a line for each port that opened or closed
// between two readings, in port order.
func portChanges(before, after map[int]bool) []string {
	changed := map[int]string{}
	for port := range after {
		if !before[port] {
			changed[port] = signOpened
		}
	}
	for port := range before {
		if !after[port] {
			changed[port] = signClosed
		}
	}

	ports := make([]int, 0, len(changed))
	for port := range changed {
		ports = append(ports, port)
	}
	sort.Ints(ports)

	lines := make([]string, 0, len(ports))
	for _, port := range ports {
		lines = append(lines, changed[port]+strconv.Itoa(port))
	}
	return lines
}
