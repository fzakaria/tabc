// Package console makes the terminal a container's program runs on as
// its engine sees that terminal. An engine that wants a terminal gives
// the runtime a socket and expects the controlling end of a pty sent
// over it. With runc the program holds the pty's other end. Here the
// program's terminal is a pty inside the guest, so the supervisor holds
// the other end and carries bytes between the two.
package console
