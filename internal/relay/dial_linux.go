//go:build linux

package relay

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// reuseControl sets SO_REUSEADDR + SO_REUSEPORT so the relay can bind the same
// port P for BOTH the joiner listener (0.0.0.0:P) and, as the source port, the
// outbound bnet connection to pvpgn. Binding that source port is what makes
// pvpgn advertise the game at <relay's own IP>:P (game addr/port default to the
// bnet TCP peer; verified in connection.cpp:371/376 and live).
func reuseControl(network, address string, c syscall.RawConn) error {
	var serr error
	if err := c.Control(func(fd uintptr) {
		if e := unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1); e != nil {
			serr = e
			return
		}
		serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
	}); err != nil {
		return err
	}
	return serr
}
