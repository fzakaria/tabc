package console

import (
	"fmt"
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
