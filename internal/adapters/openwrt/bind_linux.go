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

// bindToTunnel pins a probe socket to the tunnel's interface and marks it as
// traffic sent to the tunnel, so it takes the connection's own table (#47):
// the main table no longer holds the tunnel's routes, and the interface alone
// does not select the table.
func bindToTunnel(ifname string) func(network, address string, c syscall.RawConn) error {
	bind := bindToDevice(ifname)
	return func(network, address string, c syscall.RawConn) error {
		if err := bind(network, address, c); err != nil || ifname == "" {
			return err
		}
		var serr error
		if err := c.Control(func(fd uintptr) {
			serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_MARK, tunnelMarkValue)
		}); err != nil {
			return err
		}
		return serr
	}
}
