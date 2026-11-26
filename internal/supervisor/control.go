package supervisor

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The control socket is how `tabc start` and `tabc kill`, each a short
// process of its own, reach the supervisor of the container they name.
// A request is one line, and so is the answer.

// The requests, and the answer to one that worked. Any other answer is
// the error.
const (
	requestStart  = "start"
	requestKill   = "kill"
	requestResize = "resize"
	// requestExec runs a second program (exec.go). The connection that
	// asks stays open and becomes the program's input and output.
	requestExec = "exec"
	answerOK    = "ok"
)

// maxSocketPath is the longest socket path dialled as it is. The limit
// of a socket's address is 104 bytes on macOS and 108 on Linux.
const maxSocketPath = 100

// controlNetwork is the kind of socket the control socket is, and
// controlTimeout how long a request or its answer may take.
const (
	controlNetwork = "unix"
	controlTimeout = 30 * time.Second
)

// Request sends one request to the supervisor listening at path and
// returns its error, if the supervisor answered with one.
func Request(path string, words ...string) error {
	connection, err := dialControl(path)
	if err != nil {
		return err
	}
	defer connection.Close()
	connection.SetDeadline(time.Now().Add(controlTimeout))

	if _, err := fmt.Fprintln(connection, strings.Join(words, " ")); err != nil {
		return err
	}
	return readAnswer(bufio.NewReader(connection))
}

// dialControl connects to the supervisor listening at path.
func dialControl(path string) (net.Conn, error) {
	// A socket's address holds about a hundred bytes, and a state
	// directory under an engine's root with a 64-character id comes
	// close. A path that long is reached by its bare name from its
	// directory, as the supervisor binds it: the short-lived commands
	// that get here do nothing afterwards that depends on where they
	// stand. A path that fits is dialled as it is, which is what a
	// daemon serving many requests at once needs, since the directory a
	// process stands in is the whole process's.
	address := path
	if len(path) > maxSocketPath {
		if err := os.Chdir(filepath.Dir(path)); err != nil {
			return nil, fmt.Errorf("the container's state directory: %w", err)
		}
		address = filepath.Base(path)
	}
	connection, err := net.DialTimeout(controlNetwork, address, controlTimeout)
	if err != nil {
		return nil, fmt.Errorf("the container's supervisor is not running: %w", err)
	}
	return connection, nil
}

// readAnswer reads a supervisor's answer to a request and returns the
// error the answer is, if the answer is one.
func readAnswer(reader *bufio.Reader) error {
	answer, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("the supervisor did not answer: %w", err)
	}
	answer = strings.TrimSpace(answer)
	if answer != answerOK {
		return fmt.Errorf("%s", answer)
	}
	return nil
}

// Start asks a supervisor to run the container's program.
func Start(path string) error {
	return Request(path, requestStart)
}

// Kill asks a supervisor to send the program a signal, by the name
// signals.Parse returns.
func Kill(path, signal string) error {
	return Request(path, requestKill, signal)
}

// Resize asks a supervisor to tell the program its terminal's new size.
func Resize(path string, cols, rows int) error {
	return Request(path, requestResize, strconv.Itoa(cols), strconv.Itoa(rows))
}

// takeover serves a request that keeps its connection: the request's
// words, the connection, and the reader the request's line was read
// with. A takeover writes the answer itself.
type takeover func(connection net.Conn, reader *bufio.Reader, words []string)

// serveControl answers requests on the control socket by handing each
// to handle, whose error, if any, is the answer. A request named in
// takeovers is handed its connection instead.
func serveControl(listener net.Listener, handle func(words []string) error, takeovers map[string]takeover) {
	for {
		connection, err := listener.Accept()
		if err != nil {
			return
		}

		go func() {
			defer connection.Close()
			connection.SetDeadline(time.Now().Add(controlTimeout))

			reader := bufio.NewReader(connection)
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			words := strings.Fields(line)
			if len(words) > 0 {
				if take, has := takeovers[words[0]]; has {
					take(connection, reader, words)
					return
				}
			}
			if err := handle(words); err != nil {
				fmt.Fprintln(connection, err.Error())
				return
			}
			fmt.Fprintln(connection, answerOK)
		}()
	}
}
