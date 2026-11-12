//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package discovery

import (
	"context"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// listenMDNS opens UDP port 5353 on every address, shared with the system's own responder
// (mDNSResponder, Avahi): SO_REUSEADDR, and SO_REUSEPORT, which the BSDs and macOS need for it.
func listenMDNS() (*net.UDPConn, error) {
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var serr error
		err := c.Control(func(fd uintptr) {
			if serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1); serr == nil {
				serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
			}
		})
		if err != nil {
			return err
		}
		return serr
	}}
	pc, err := lc.ListenPacket(context.Background(), "udp4", net.JoinHostPort("0.0.0.0", "5353"))
	if err != nil {
		return nil, err
	}
	return pc.(*net.UDPConn), nil
}
