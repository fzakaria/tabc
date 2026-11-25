package console

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// TestRawPty checks that bytes written to one end of a raw pty arrive
// at the other end unchanged and are not echoed back, by writing a line
// ending both ways.
func TestRawPty(t *testing.T) {
	controller, terminal, err := Open()
	if err != nil {
		t.Skipf("no pty here: %v", err)
	}
	defer controller.Close()
	defer terminal.Close()
	if err := MakeRaw(terminal); err != nil {
		t.Fatal(err)
	}

	// Engine to program: a carriage return stays a carriage return.
	if _, err := controller.Write([]byte("a\r")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 8)
	count, err := terminal.Read(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if string(buffer[:count]) != "a\r" {
		t.Errorf("the program's end read %q", buffer[:count])
	}

	// Program to engine: a newline stays a newline, and nothing the
	// engine wrote comes back first.
	if _, err := terminal.Write([]byte("b\n")); err != nil {
		t.Fatal(err)
	}
	count, err = controller.Read(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if string(buffer[:count]) != "b\n" {
		t.Errorf("the engine's end read %q", buffer[:count])
	}
}

// TestSizeFollowsController checks that a size the engine sets on its
// end is the size the supervisor reads on the other end.
func TestSizeFollowsController(t *testing.T) {
	controller, terminal, err := Open()
	if err != nil {
		t.Skipf("no pty here: %v", err)
	}
	defer controller.Close()
	defer terminal.Close()

	size := &unix.Winsize{Col: 132, Row: 43}
	if err := unix.IoctlSetWinsize(int(controller.Fd()), unix.TIOCSWINSZ, size); err != nil {
		t.Fatal(err)
	}
	cols, rows, err := Size(terminal)
	if err != nil {
		t.Fatal(err)
	}
	if cols != 132 || rows != 43 {
		t.Errorf("size %dx%d, want 132x43", cols, rows)
	}
}

// TestSend checks that the engine's side of a console socket receives a
// working descriptor for the pty: a listener stands in for the engine,
// takes the descriptor from the control message, and reads through it
// what the program's end wrote.
func TestSend(t *testing.T) {
	controller, terminal, err := Open()
	if err != nil {
		t.Skipf("no pty here: %v", err)
	}
	defer controller.Close()
	defer terminal.Close()
	if err := MakeRaw(terminal); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "console.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	received := make(chan *os.File, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			received <- nil
			return
		}
		defer connection.Close()

		data := make([]byte, 64)
		control := make([]byte, unix.CmsgSpace(4))
		_, controlLength, _, _, err := connection.(*net.UnixConn).ReadMsgUnix(data, control)
		if err != nil {
			received <- nil
			return
		}
		messages, err := unix.ParseSocketControlMessage(control[:controlLength])
		if err != nil || len(messages) != 1 {
			received <- nil
			return
		}
		descriptors, err := unix.ParseUnixRights(&messages[0])
		if err != nil || len(descriptors) != 1 {
			received <- nil
			return
		}
		received <- os.NewFile(uintptr(descriptors[0]), "received")
	}()

	if err := Send(path, controller); err != nil {
		t.Fatal(err)
	}
	engine := <-received
	if engine == nil {
		t.Fatal("the engine's side got no descriptor")
	}
	defer engine.Close()

	if _, err := terminal.Write([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 8)
	count, err := engine.Read(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if string(buffer[:count]) != "hi" {
		t.Errorf("the engine read %q", buffer[:count])
	}
}
