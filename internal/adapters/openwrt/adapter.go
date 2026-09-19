// Package openwrt implements the core managers for OpenWrt hosts: the AmneziaWG
// tunnel engine, VeilBridge's own nftables routing (it does NOT depend on
// podkop), and /proc-based system info — all behind the core interfaces, so the
// API and the web UI never see uci, ubus or nft.
//
// OpenWrt is the only supported platform (see internal/adapters/detect.go).
// Trying the panel on another OS is what -demo is for.
//
// Layout: this file is the product constructor, wiring.go holds the adapter
// struct and the injectable constructor, and one file per manager (vpn.go,
// routing.go, system.go). See the architecture notes in CONTRIBUTING.md.
package openwrt

import (
	"github.com/veilbridge-os/veilbridge/internal/core"
	"github.com/veilbridge-os/veilbridge/internal/vpn/amneziawg"
)

// New builds the OpenWrt adapter backed by the config store at the given path
// (config.DefaultPath if empty).
//
// It uses the kernel-TUN engine: the tunnel shows up as a real interface (awg0)
// that the host's nftables can forward through transparently, which is what
// makes VeilBridge a router-wide gateway rather than a proxy for its own
// traffic. Devices without kmod-tun need the userspace netstack engine instead
// — that path is reachable through NewWithEngine and is not yet auto-detected.
func New(configPath string) core.Adapter {
	return NewWithEngine(configPath, amneziawg.NewKernelEngine(), "openwrt")
}
