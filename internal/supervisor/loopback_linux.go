package supervisor

import "golang.org/x/sys/unix"

// loopbackDevice is the loopback device's name.
const loopbackDevice = "lo"

// raiseLoopback brings the loopback device up. A network namespace made
// for the container starts with loopback down, and the browser reaches
// the supervisor over it.
func raiseLoopback() error {
	socket, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(socket)

	request, err := unix.NewIfreq(loopbackDevice)
	if err != nil {
		return err
	}
	if err := unix.IoctlIfreq(socket, unix.SIOCGIFFLAGS, request); err != nil {
		return err
	}
	if request.Uint16()&unix.IFF_UP != 0 {
		return nil
	}
	request.SetUint16(request.Uint16() | unix.IFF_UP)
	return unix.IoctlIfreq(socket, unix.SIOCSIFFLAGS, request)
}
