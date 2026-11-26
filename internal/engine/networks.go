package engine

import (
	"fmt"
	"sort"
	"time"
)

// builtinNetworks are the networks every Docker has, which clients
// expect to find. A container on one of them is on no tabc network: it
// reaches the outside through its supervisor and is reached through its
// published ports.
var builtinNetworks = []string{"bridge", "host", "none"}

// driverBridge is the driver a network a client makes is said to have.
const driverBridge = "bridge"

// Network is a set of containers that reach each other by name
// (internal/netreg).
type Network struct {
	ID      string
	Name    string
	Driver  string
	Labels  map[string]string
	Created time.Time
	Builtin bool
}

// CreateNetwork makes a network. A second network of one name is
// refused: names are how networks are found.
func (e *Engine) CreateNetwork(name string, labels map[string]string) (*Network, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if _, has := e.networks[name]; has {
		return nil, fmt.Errorf("%w: network with name %s already exists", ErrConflict, name)
	}
	network := &Network{ID: newID(), Name: name, Driver: driverBridge, Labels: labels, Created: time.Now()}
	e.networks[name] = network
	e.events.emit(Event{Type: "network", Action: "create", ID: network.ID, Attributes: map[string]string{"name": name}})
	return network, nil
}

// Network returns a network by name, by id or by the start of its id.
func (e *Engine) Network(reference string) (*Network, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.networkLocked(reference)
}

func (e *Engine) networkLocked(reference string) (*Network, error) {
	if network, has := e.networks[reference]; has {
		return network, nil
	}
	for _, network := range e.networks {
		if reference != "" && len(reference) <= len(network.ID) && network.ID[:len(reference)] == reference {
			return network, nil
		}
	}
	return nil, fmt.Errorf("%w: network %s not found", ErrNotFound, reference)
}

// Networks returns every network, by name.
func (e *Engine) Networks() []*Network {
	e.mu.Lock()
	defer e.mu.Unlock()
	networks := make([]*Network, 0, len(e.networks))
	for _, network := range e.networks {
		networks = append(networks, network)
	}
	sort.Slice(networks, func(i, j int) bool { return networks[i].Name < networks[j].Name })
	return networks
}

// RemoveNetwork forgets a network nobody is on.
func (e *Engine) RemoveNetwork(reference string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	network, err := e.networkLocked(reference)
	if err != nil {
		return err
	}
	if network.Builtin {
		return fmt.Errorf("%w: %s is a pre-defined network and cannot be removed", ErrConflict, network.Name)
	}
	for _, container := range e.containers {
		if _, on := container.Spec.Networks[network.Name]; on {
			return fmt.Errorf("%w: network %s has active endpoints", ErrConflict, network.Name)
		}
	}
	delete(e.networks, network.Name)
	e.events.emit(Event{Type: "network", Action: "destroy", ID: network.ID, Attributes: map[string]string{"name": network.Name}})
	return nil
}

// Connect puts a container that has not started yet on a network under
// the given names.
func (e *Engine) Connect(networkReference, containerReference string, aliases []string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	network, err := e.networkLocked(networkReference)
	if err != nil {
		return err
	}
	container, err := e.containerLocked(containerReference)
	if err != nil {
		return err
	}
	if container.Spec.Networks == nil {
		container.Spec.Networks = map[string][]string{}
	}
	container.Spec.Networks[network.Name] = aliases
	return nil
}

// Disconnect takes a container off a network.
func (e *Engine) Disconnect(networkReference, containerReference string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	network, err := e.networkLocked(networkReference)
	if err != nil {
		return err
	}
	container, err := e.containerLocked(containerReference)
	if err != nil {
		return err
	}
	delete(container.Spec.Networks, network.Name)
	return nil
}
