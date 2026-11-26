package dockerapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/fzakaria/tabc/internal/engine"
	"github.com/fzakaria/tabc/internal/supervisor"
)

// `docker exec` is two requests: one that describes the program and is
// answered with an id, and one that runs the program under that id and
// carries its input and output, the way attach carries a container's.

// consoleSizeLength is how many numbers a terminal's size is given as:
// its rows, then its columns.
const consoleSizeLength = 2

// consoleSize returns a size given as rows and columns, or zeros.
func consoleSize(size []int) (cols, rows int) {
	if len(size) != consoleSizeLength {
		return 0, 0
	}
	return size[1], size[0]
}

func (s *Server) execCreate(w http.ResponseWriter, r *http.Request, args []string) {
	var request struct {
		Cmd         []string
		Env         []string
		WorkingDir  string
		User        string
		Tty         bool
		AttachStdin bool
		ConsoleSize []int
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	cols, rows := consoleSize(request.ConsoleSize)
	created, err := s.Engine.CreateExec(args[0], engine.ExecSpec{
		Cmd:         request.Cmd,
		Env:         request.Env,
		WorkingDir:  request.WorkingDir,
		User:        request.User,
		Tty:         request.Tty,
		Cols:        cols,
		Rows:        rows,
		AttachStdin: request.AttachStdin,
	})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"Id": created.ID})
}

// frameWriter writes a program's output to a client as one of the two
// streams. A client that is gone ends the session, and with the session
// the program.
type frameWriter struct {
	to      io.Writer
	stream  byte
	tty     bool
	session *supervisor.ExecSession
}

func (f frameWriter) Write(data []byte) (int, error) {
	if err := writeFrame(f.to, engine.Frame{Stream: f.stream, Data: data}, f.tty); err != nil {
		f.session.Close()
		return 0, err
	}
	return len(data), nil
}

// execStart runs a second program. Unless the client asked to be let
// go, the connection stops being HTTP after the first answer and
// carries the program's input and output.
func (s *Server) execStart(w http.ResponseWriter, r *http.Request, args []string) {
	var request struct {
		Detach      bool
		ConsoleSize []int
	}
	// An empty body is a request with nothing set.
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil && err != io.EOF {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	program, err := s.Engine.Exec(args[0])
	if err != nil {
		fail(w, err)
		return
	}
	if cols, rows := consoleSize(request.ConsoleSize); cols > 0 && rows > 0 {
		program.Resize(cols, rows)
	}
	tty := program.Spec.Tty

	session, err := s.Engine.StartExec(program)
	if err != nil {
		fail(w, err)
		return
	}

	// Let go: the program runs with no input, and its output goes
	// nowhere.
	if request.Detach {
		go func() {
			defer session.Close()
			session.CloseInput()
			status, _ := session.Wait(io.Discard, io.Discard)
			program.Finish(status)
		}()
		w.WriteHeader(http.StatusOK)
		return
	}
	defer session.Close()

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		program.Finish(supervisor.ExitFailed)
		writeError(w, http.StatusInternalServerError, fmt.Errorf("this connection cannot be attached"))
		return
	}
	connection, buffered, err := hijacker.Hijack()
	if err != nil {
		program.Finish(supervisor.ExitFailed)
		return
	}
	defer connection.Close()

	// A client that asked to upgrade is told the upgrade happened; an
	// older one is simply answered.
	if r.Header.Get("Upgrade") != "" {
		fmt.Fprintf(connection, "HTTP/1.1 101 UPGRADED\r\nContent-Type: %s\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n", streamType(tty))
	} else {
		fmt.Fprintf(connection, "HTTP/1.1 200 OK\r\nContent-Type: %s\r\n\r\n", streamType(tty))
	}

	// The client's input, which may have begun arriving with the
	// request. Without a terminal its end is passed on.
	go func() {
		if program.Spec.AttachStdin {
			io.Copy(session, buffered.Reader)
		}
		if !tty {
			session.CloseInput()
		}
	}()

	status, _ := session.Wait(
		frameWriter{to: connection, stream: engine.StreamStdout, tty: tty, session: session},
		frameWriter{to: connection, stream: engine.StreamStderr, tty: tty, session: session},
	)
	program.Finish(status)

	// The program is done. Its output's end is said by closing the
	// sending side, so a client still reading sees an end and not a
	// reset.
	if closer, ok := connection.(interface{ CloseWrite() error }); ok {
		closer.CloseWrite()
	}
}

func (s *Server) execResize(w http.ResponseWriter, r *http.Request, args []string) {
	program, err := s.Engine.Exec(args[0])
	if err != nil {
		fail(w, err)
		return
	}
	query := r.URL.Query()
	rows, _ := strconv.Atoi(query.Get("h"))
	cols, _ := strconv.Atoi(query.Get("w"))
	if rows > 0 && cols > 0 {
		program.Resize(cols, rows)
	}
	w.WriteHeader(http.StatusOK)
}

// execInspect answers what a client asks once the program is done: its
// exit status.
func (s *Server) execInspect(w http.ResponseWriter, r *http.Request, args []string) {
	program, err := s.Engine.Exec(args[0])
	if err != nil {
		fail(w, err)
		return
	}
	state := program.State()
	writeJSON(w, http.StatusOK, map[string]any{
		"ID":          program.ID,
		"ContainerID": program.Container.ID,
		"Running":     state.Running,
		"ExitCode":    state.ExitCode,
		"OpenStdin":   program.Spec.AttachStdin,
		"ProcessConfig": map[string]any{
			"tty":        program.Spec.Tty,
			"entrypoint": program.Spec.Cmd[0],
			"arguments":  program.Spec.Cmd[1:],
			"user":       program.Spec.User,
		},
	})
}
