package supervisor

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The control socket is how `tabc start` and `tabc kill`, each a short
// process of its own, reach the supervisor of the container they name.
// A request is one line, and so is the answer.

// The requests, and the answer to one that worked. Any other answer is
// the error.
const (
	requestStart = "start"
	requestKill  = "kill"
	answerOK     = "ok"
)

// controlNetwork is the kind of socket the control socket is, and
// controlTimeout how long a request or its answer may take.
const (
	controlNetwork = "unix"
	controlTimeout = 30 * time.Second
)

// Request sends one request to the supervisor listening at path and
// returns its error, if the supervisor answered with one.
func Request(path string, words ...string) error {
	// A socket's address holds about a hundred bytes, and a state
	// directory under an engine's root with a 64-character id comes
	// close. The socket is reached by its bare name from its directory,
	// as the supervisor binds it. The commands that call this do nothing
	// else afterwards that depends on where they stand.
	if err := os.Chdir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("the container's state directory: %w", err)
	}
	connection, err := net.DialTimeout(controlNetwork, filepath.Base(path), controlTimeout)
	if err != nil {
		return fmt.Errorf("the container's supervisor is not running: %w", err)
	}
	defer connection.Close()
	connection.SetDeadline(time.Now().Add(controlTimeout))

	if _, err := fmt.Fprintln(connection, strings.Join(words, " ")); err != nil {
		return err
	}
	answer, err := bufio.NewReader(connection).ReadString('\n')
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

// serveControl answers requests on the control socket by handing each
// to handle, whose error, if any, is the answer.
func serveControl(listener net.Listener, handle func(words []string) error) {
	for {
		connection, err := listener.Accept()
		if err != nil {
			return
		}

		go func() {
			defer connection.Close()
			connection.SetDeadline(time.Now().Add(controlTimeout))

			line, err := bufio.NewReader(connection).ReadString('\n')
			if err != nil {
				return
			}
			if err := handle(strings.Fields(line)); err != nil {
				fmt.Fprintln(connection, err.Error())
				return
			}
			fmt.Fprintln(connection, answerOK)
		}()
	}
}
