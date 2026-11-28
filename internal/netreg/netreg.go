// Package netreg is how containers find each other when no engine gave
// them a network: a registry of names on disk.
//
// Under Docker a container has an address on Docker's bridge and
// Docker's DNS knows its name. With no engine there is neither. A tabc
// network is then a directory, and a container on the network is a file
// in it for each name the container goes by. The file gives the
// container an address that exists nowhere but in this registry, and
// says where the container's supervisor takes connections.
//
// A guest that looks up "redis" is answered with redis's address from
// the registry. When the guest then connects to that address, its
// supervisor finds the address in the registry and hands the
// connection to redis's supervisor, which carries it into redis's
// guest. No packet ever travels to the address itself.
package netreg

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// dirName is the registry's directory under the runtime's root. The dot
// keeps it apart from containers, whose ids may not start with one.
const dirName = ".networks"

// lockName is the file a network's directory is locked by while an
// address is chosen, and entrySuffix what a name's file ends in.
const (
	lockName    = ".lock"
	entrySuffix = ".json"
)

// The permissions of the registry. An entry holds the address of a
// supervisor's relay with its secret, so only the owner reads it.
const (
	dirMode   = 0o700
	entryMode = 0o600
)

// Addresses come from 10.89.0.0/16, a private range no network a
// container is likely to want to reach uses: the first three host
// numbers of a /24 are skipped as a real network would reserve them,
// and each network has room for hostsPerNetwork containers.
const (
	addressPrefix   = "10.89."
	firstHost       = 2
	hostsPerOctet   = 254
	hostsPerNetwork = 250 * hostsPerOctet
)

// Entry is one name on a network.
type Entry struct {
	// Name is the name, and Container the id of the container it
	// belongs to.
	Name      string `json:"name"`
	Container string `json:"container"`
	// IP is the container's address on the network.
	IP string `json:"ip"`
	// Relay is where the container's supervisor takes connections for
	// the container's guest: a WebSocket address, secret included.
	Relay string `json:"relay"`
}

// Dir returns a network's directory under the runtime's root.
func Dir(root, network string) string {
	return filepath.Join(root, dirName, network)
}

// validName reports whether a network or container name can be a file
// name: not empty, no path separator, no leading dot.
func validName(name string) bool {
	return name != "" && name == filepath.Base(name) && !strings.HasPrefix(name, ".")
}

// Register puts a container on a network under each of its names and
// returns the files written, for removal when the container goes, and
// the address the container was given. A container already on the
// network keeps its address.
func Register(root, network, container, relay string, names []string) (files []string, ip string, err error) {
	if !validName(network) {
		return nil, "", fmt.Errorf("invalid network name %q", network)
	}
	dir := Dir(root, network)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return nil, "", err
	}

	// One container at a time chooses an address.
	unlock, err := lock(dir)
	if err != nil {
		return nil, "", err
	}
	defer unlock()

	entries, err := List(root, network)
	if err != nil {
		return nil, "", err
	}
	ip = addressFor(container, entries)
	if ip == "" {
		return nil, "", fmt.Errorf("network %q is full", network)
	}

	for _, name := range names {
		if !validName(name) {
			continue
		}
		entry := Entry{Name: name, Container: container, IP: ip, Relay: relay}
		data, err := json.Marshal(entry)
		if err != nil {
			return files, ip, err
		}
		path := filepath.Join(dir, name+entrySuffix)
		if err := os.WriteFile(path, data, entryMode); err != nil {
			return files, ip, err
		}
		files = append(files, path)
	}
	return files, ip, nil
}

// addressFor returns the address a container has on a network, or the
// lowest one nobody has.
func addressFor(container string, entries []Entry) string {
	taken := map[string]bool{}
	for _, entry := range entries {
		if entry.Container == container {
			return entry.IP
		}
		taken[entry.IP] = true
	}

	for host := 0; host < hostsPerNetwork; host++ {
		candidate := fmt.Sprintf("%s%d.%d", addressPrefix, host/hostsPerOctet, host%hostsPerOctet+firstHost)
		if !taken[candidate] {
			return candidate
		}
	}
	return ""
}

// lock takes a network's lock and returns the function that gives it
// back.
func lock(dir string) (func(), error) {
	file, err := os.OpenFile(filepath.Join(dir, lockName), os.O_CREATE|os.O_RDWR, entryMode)
	if err != nil {
		return nil, err
	}
	if err := lockFile(file); err != nil {
		file.Close()
		return nil, err
	}
	return func() {
		unlockFile(file)
		file.Close()
	}, nil
}

// List returns every name on a network. A network nobody is on is
// empty, not an error.
func List(root, network string) ([]Entry, error) {
	listed, err := os.ReadDir(Dir(root, network))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var entries []Entry
	for _, item := range listed {
		if !strings.HasSuffix(item.Name(), entrySuffix) {
			continue
		}
		entry, ok := read(filepath.Join(Dir(root, network), item.Name()))
		if ok {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

// read returns the entry in a file. A file that is gone or half
// written, a container leaving as another looks, is no entry.
func read(path string) (Entry, bool) {
	var entry Entry
	data, err := os.ReadFile(path)
	if err != nil {
		return entry, false
	}
	if err := json.Unmarshal(data, &entry); err != nil {
		return entry, false
	}
	return entry, true
}

// Lookup returns the entry for a name on any of the networks given,
// first match first.
func Lookup(root string, networks []string, name string) (Entry, bool) {
	if !validName(name) {
		return Entry{}, false
	}
	for _, network := range networks {
		if entry, ok := read(filepath.Join(Dir(root, network), name+entrySuffix)); ok {
			return entry, true
		}
	}
	return Entry{}, false
}

// ByAddress returns an entry whose address is ip on any of the networks
// given: the container a connection to that address is meant for.
func ByAddress(root string, networks []string, ip string) (Entry, bool) {
	if net.ParseIP(ip) == nil || !strings.HasPrefix(ip, addressPrefix) {
		return Entry{}, false
	}
	for _, network := range networks {
		entries, err := List(root, network)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IP == ip {
				return entry, true
			}
		}
	}
	return Entry{}, false
}

// Unregister removes the files Register wrote.
func Unregister(files []string) {
	for _, file := range files {
		os.Remove(file)
	}
}
