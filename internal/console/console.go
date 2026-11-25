// Package console makes the terminal a container's program runs on as
// its engine sees that terminal. An engine that wants a terminal gives
// the runtime a socket and expects the controlling end of a pty sent
// over it. With runc the program holds the pty's other end. Here the
// program's terminal is a pty inside the guest, so the supervisor holds
// the other end and carries bytes between the two.
package console

import (
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// ptmxPath opens a new pty, and ptsPattern names a pty's other end by
// its number.
const (
	ptmxPath   = "/dev/ptmx"
	ptsPattern = "/dev/pts/%d"
)

// unlocked is the value TIOCSPTLCK takes to unlock a pty's other end.
const unlocked = 0

// socketNetwork is the kind of socket an engine's console socket is.
const socketNetwork = "unix"

// Open makes a pty and returns its two ends: the controlling end, which
// goes to the engine, and the end the supervisor reads and writes.
func Open() (controller, terminal *os.File, err error) {
	controller, err = os.OpenFile(ptmxPath, os.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}

	// The other end exists once unlocked, under the pty's number.
	fd := int(controller.Fd())
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, unlocked); err != nil {
		controller.Close()
		return nil, nil, fmt.Errorf("unlocking the pty: %w", err)
	}
	number, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		controller.Close()
		return nil, nil, fmt.Errorf("naming the pty: %w", err)
	}

	terminal, err = os.OpenFile(fmt.Sprintf(ptsPattern, number), os.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		controller.Close()
		return nil, nil, err
	}
	return controller, terminal, nil
}

// MakeRaw stops the pty from editing lines, echoing or translating
// bytes. The guest's pty does all of that for the program, and a second
// line discipline on the way would echo every key twice.
func MakeRaw(terminal *os.File) error {
	fd := int(terminal.Fd())
	termios, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return err
	}

	termios.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	termios.Oflag &^= unix.OPOST
	termios.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	termios.Cflag &^= unix.CSIZE | unix.PARENB
	termios.Cflag |= unix.CS8
	termios.Cc[unix.VMIN] = 1
	termios.Cc[unix.VTIME] = 0

	return unix.IoctlSetTermios(fd, unix.TCSETS, termios)
}

// Size returns the terminal's size in character cells, as the engine
// last set it on the controlling end.
func Size(terminal *os.File) (cols, rows int, err error) {
	size, err := unix.IoctlGetWinsize(int(terminal.Fd()), unix.TIOCGWINSZ)
	if err != nil {
		return 0, 0, err
	}
	return int(size.Col), int(size.Row), nil
}

// Send passes the pty's controlling end to the engine over the socket
// at path, as the runtime specification's console socket works: one
// message carrying the file descriptor.
func Send(path string, controller *os.File) error {
	connection, err := net.Dial(socketNetwork, path)
	if err != nil {
		return fmt.Errorf("connecting to the console socket: %w", err)
	}
	defer connection.Close()

	socket, ok := connection.(*net.UnixConn)
	if !ok {
		return fmt.Errorf("the console socket %s is not a unix socket", path)
	}

	// The message's bytes are the descriptor's name, by convention; the
	// descriptor itself rides in the control message.
	name := []byte(controller.Name())
	rights := unix.UnixRights(int(controller.Fd()))
	if _, _, err := socket.WriteMsgUnix(name, rights, nil); err != nil {
		return fmt.Errorf("sending the terminal to the engine: %w", err)
	}
	return nil
}
