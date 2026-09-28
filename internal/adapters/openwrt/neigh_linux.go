//go:build linux

package openwrt

import (
	"net"
	"syscall"
)

// kernelNeighbours asks the kernel for its neighbour table over netlink —
// the table `ip neigh` prints, without running `ip`: the adapter's list of
// programs it may execute stays as short as it is (uci.go), and a read-only
// question does not need a tool that can also take links down.
func kernelNeighbours(dev string) ([]neighbour, error) {
	ifi, err := net.InterfaceByName(dev)
	if err != nil {
		return nil, err
	}
	raw, err := syscall.NetlinkRIB(syscall.RTM_GETNEIGH, syscall.AF_UNSPEC)
	if err != nil {
		return nil, err
	}
	msgs, err := syscall.ParseNetlinkMessage(raw)
	if err != nil {
		return nil, err
	}
	var out []neighbour
	for _, m := range msgs {
		if m.Header.Type != syscall.RTM_NEWNEIGH {
			continue
		}
		if n, ok := parseNeighMsg(m.Data, ifi.Index); ok {
			out = append(out, n)
		}
	}
	return out, nil
}
