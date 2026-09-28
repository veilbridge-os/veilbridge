//go:build !linux

package openwrt

import "github.com/veilbridge-os/veilbridge/internal/core"

// kernelNeighbours needs Linux netlink; on a development machine the device
// list simply has no neighbour source.
func kernelNeighbours(string) ([]neighbour, error) { return nil, core.ErrNotImplemented }
