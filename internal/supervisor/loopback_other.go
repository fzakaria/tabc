//go:build !linux

package supervisor

// raiseLoopback has nothing to do where there are no network
// namespaces: the host's loopback is up.
func raiseLoopback() error {
	return nil
}
