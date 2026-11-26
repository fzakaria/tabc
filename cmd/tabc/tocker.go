package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/fzakaria/tabc/internal/state"
	"github.com/fzakaria/tabc/internal/supervisor"
)

// tocker is the Docker command line pointed at tabc. It makes sure a
// tabc daemon is running and then becomes the real `docker`, with
// DOCKER_HOST naming the daemon's socket:
//
//	tocker run -it alpine sh
//	tocker compose up
//
// Nothing of Docker's is reimplemented here. The docker program does
// what it always does, and the daemon it talks to starts tabs.

// tockerName is the name the tabc binary is run under to be tocker, and
// commandTocker the command that does the same.
const (
	tockerName    = "tocker"
	commandTocker = "tocker"
	commandDaemon = "daemon"
)

// tocker's own flags, which come before the docker command.
const (
	tockerDashboard = "--dashboard"
	tockerStop      = "--stop"
)

// envDocker names the docker program to run, and envDockerHost is how
// that program is told where its daemon is.
const (
	envDocker     = "TABC_DOCKER"
	envDockerHost = "DOCKER_HOST"
	dockerProgram = "docker"
)

// dockerBuilt is the docker program the package that built tabc pins
// (nix/package.nix), empty in a plain `go build`.
var dockerBuilt string

// daemonStartWait is how long a daemon gets to start answering, and
// daemonPoll how often tocker looks.
const (
	daemonStartWait = 15 * time.Second
	daemonPoll      = 50 * time.Millisecond
)

// isTocker reports whether the binary was run under tocker's name.
func isTocker() bool {
	return filepath.Base(os.Args[0]) == tockerName
}

// tocker runs a docker command against tabc's daemon. It returns only
// on failure: on success the process has become docker.
func tocker(args []string) error {
	wantDashboard := false
	for len(args) > 0 {
		if args[0] == tockerStop {
			return stopDaemon()
		}
		if args[0] != tockerDashboard {
			break
		}
		wantDashboard = true
		args = args[1:]
	}

	record, running := readDaemon()
	if running && wantDashboard && record.Dashboard == "" {
		return errors.New("the tabc daemon is running without a dashboard; run `tocker --stop` first")
	}
	if !running {
		var err error
		if record, err = startDaemon(wantDashboard); err != nil {
			return err
		}
	}
	if record.Dashboard != "" {
		fmt.Fprintf(os.Stderr, "tocker: containers run in the dashboard at %s\n", record.Dashboard)
	}

	// With no docker command the dashboard was the point.
	if len(args) == 0 {
		if record.Dashboard != "" {
			return nil
		}
		return errors.New("usage: tocker [--dashboard] <docker command...> | tocker --stop")
	}

	docker, err := dockerPath()
	if err != nil {
		return err
	}
	environment := append(os.Environ(), envDockerHost+"=unix://"+record.Socket)
	return syscall.Exec(docker, append([]string{dockerProgram}, args...), environment)
}

// dockerPath returns the docker program to become: the environment's,
// the built-in one, or the one on the PATH.
func dockerPath() (string, error) {
	if path := os.Getenv(envDocker); path != "" {
		return path, nil
	}
	if dockerBuilt != "" {
		return dockerBuilt, nil
	}
	path, err := exec.LookPath(dockerProgram)
	if err != nil {
		return "", errors.New("tocker runs the docker command line, and none is installed; set " + envDocker)
	}
	return path, nil
}

// startDaemon starts a daemon that outlives this command and waits for
// it to answer.
func startDaemon(withDashboard bool) (daemonRecord, error) {
	self, err := os.Executable()
	if err != nil {
		return daemonRecord{}, err
	}
	root := state.Root("")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return daemonRecord{}, err
	}
	logFile, err := os.OpenFile(filepath.Join(root, daemonLog), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return daemonRecord{}, err
	}
	defer logFile.Close()

	args := []string{commandDaemon}
	if withDashboard {
		args = append(args, flagDashboard)
	}
	process := exec.Command(self, args...)
	process.Stdout = logFile
	process.Stderr = logFile
	process.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := process.Start(); err != nil {
		return daemonRecord{}, err
	}
	process.Process.Release()

	deadline := time.Now().Add(daemonStartWait)
	for time.Now().Before(deadline) {
		if record, running := readDaemon(); running {
			return record, nil
		}
		time.Sleep(daemonPoll)
	}
	return daemonRecord{}, fmt.Errorf("the tabc daemon did not start; see %s", logFile.Name())
}

// stopDaemon tells the running daemon to stop, which stops its
// containers.
func stopDaemon() error {
	record, running := readDaemon()
	if !running {
		return errors.New("no tabc daemon is running")
	}
	if err := syscall.Kill(record.Pid, syscall.SIGTERM); err != nil {
		return err
	}

	deadline := time.Now().Add(daemonStartWait)
	for time.Now().Before(deadline) {
		if _, running := readDaemon(); !running {
			return nil
		}
		time.Sleep(daemonPoll)
	}
	return errors.New("the tabc daemon did not stop")
}

// openDashboard asks the desktop to open the dashboard, and says so if
// it cannot: the address is on the terminal either way.
func openDashboard(address string) {
	if err := supervisor.OpenInBrowser(address); err != nil {
		fmt.Printf("tabc: could not open a browser (%v); open the dashboard yourself\n", err)
	}
}
