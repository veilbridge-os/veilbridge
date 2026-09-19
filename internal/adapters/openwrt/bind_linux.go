//go:build linux

package openwrt

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// bindToDevice returns a net.Dialer Control hook that pins the socket to ifname
// via SO_BINDTODEVICE, so traffic egresses through that interface (e.g. the
// kernel tunnel awg0). Linux-only; needs CAP_NET_RAW (root). Empty ifname is a
// no-op so callers can pass it unconditionally.
func bindToDevice(ifname string) func(network, address string, c syscall.RawConn) error {
	return func(network, address string, c syscall.RawConn) error {
		if ifname == "" {
			return nil
		}
		var serr error
		if err := c.Control(func(fd uintptr) {
			serr = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, ifname)
		}); err != nil {
			return err
		}
		return serr
	}
}
