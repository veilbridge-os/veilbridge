//go:build !linux

package openwrt

import "syscall"

// bindToDevice is a no-op off Linux: SO_BINDTODEVICE is Linux-specific, and the
// kernel engine only runs on Linux targets (the router). This stub keeps the
// package building on dev machines (macOS) where only the netstack path is used.
func bindToDevice(string) func(network, address string, c syscall.RawConn) error {
	return func(string, string, syscall.RawConn) error { return nil }
}
