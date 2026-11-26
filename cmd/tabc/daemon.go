package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/fzakaria/tabc/internal/dashboard"
	"github.com/fzakaria/tabc/internal/dockerapi"
	"github.com/fzakaria/tabc/internal/engine"
	"github.com/fzakaria/tabc/internal/image"
	"github.com/fzakaria/tabc/internal/plan"
	"github.com/fzakaria/tabc/internal/state"
)

// `tabc daemon` is Docker's daemon for a machine that has none: it
// answers the Docker command line over a socket and runs each container
// in a browser tab. `tocker` (tocker.go) starts it on demand.

// The daemon's files under the runtime's root: its socket, what it
// says about itself for tocker to read, and its log.
const (
	daemonSocket = "docker.sock"
	daemonInfo   = "daemon.json"
	daemonLog    = "daemon.log"
)

// The daemon's own flags.
const (
	flagSocket    = "--socket"
	flagDashboard = "--dashboard"
	flagNoOpen    = "--no-open"
)

// socketMode lets only the daemon's user talk to it: whoever can is
// whoever can start containers.
const socketMode = 0o600

// stopOnExit is how long each container gets to stop when the daemon is
// told to.
const stopOnExit = 2 * time.Second

// daemonRecord is what a running daemon says about itself.
type daemonRecord struct {
	Pid    int    `json:"pid"`
	Socket string `json:"socket"`
	// Dashboard is the dashboard's address, "" for a daemon that runs
	// its containers in hidden browsers.
	Dashboard string `json:"dashboard"`
}

// daemonPaths returns where a daemon's socket and record are.
func daemonPaths() (socket, info string) {
	root := state.Root("")
	return filepath.Join(root, daemonSocket), filepath.Join(root, daemonInfo)
}

// readDaemon returns the record of the daemon that is running, if one
// answers on its socket.
func readDaemon() (daemonRecord, bool) {
	var record daemonRecord
	_, info := daemonPaths()
	data, err := os.ReadFile(info)
	if err != nil {
		return record, false
	}
	if err := json.Unmarshal(data, &record); err != nil {
		return record, false
	}

	connection, err := net.DialTimeout("unix", record.Socket, time.Second)
	if err != nil {
		return record, false
	}
	connection.Close()
	return record, true
}

// daemon runs the daemon until it is told to stop.
func daemon(args []string) error {
	socket, info := daemonPaths()
	withDashboard, open := false, true
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case flagSocket:
			if index+1 >= len(args) {
				return errors.New("--socket needs a path")
			}
			index++
			socket = args[index]
		case flagDashboard:
			withDashboard = true
		case flagNoOpen:
			open = false
		default:
			return fmt.Errorf("unknown flag %s", args[index])
		}
	}
	if _, running := readDaemon(); running {
		return errors.New("a tabc daemon is already running")
	}

	root := state.Root("")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	cache, err := image.CacheDir()
	if err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}

	config := engine.Config{Root: root, CacheDir: cache, DataDir: cache, Self: self}
	if withDashboard {
		config.Browser = plan.BrowserExternal
	}
	core := engine.New(config)

	// The socket. One left by a daemon that died is in the way and
	// answers nobody.
	os.Remove(socket)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer os.Remove(socket)
	if err := os.Chmod(socket, socketMode); err != nil {
		return err
	}

	record := daemonRecord{Pid: os.Getpid(), Socket: socket}
	if withDashboard {
		files, err := share()
		if err != nil {
			return err
		}
		panes, err := dashboard.Listen(core, files)
		if err != nil {
			return err
		}
		defer panes.Close()
		record.Dashboard = panes.URL()
		fmt.Printf("tabc: the dashboard is at %s\n", record.Dashboard)
		if open {
			openDashboard(record.Dashboard)
		}
	}
	data, _ := json.Marshal(record)
	if err := os.WriteFile(info, data, socketMode); err != nil {
		return err
	}
	defer os.Remove(info)

	api := &dockerapi.Server{Engine: core, Version: version, Logf: log.Printf}
	server := &http.Server{Handler: api}
	go server.Serve(listener)
	log.Printf("answering Docker's API on %s", socket)

	// Until told to stop. The containers go with the daemon: nothing
	// else would know they were there.
	caught := make(chan os.Signal, 1)
	signal.Notify(caught, syscall.SIGINT, syscall.SIGTERM)
	<-caught
	log.Printf("stopping")
	for _, container := range core.Containers() {
		core.Stop(container.ID, stopOnExit)
	}
	return server.Close()
}
