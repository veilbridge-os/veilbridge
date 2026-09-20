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
	"github.com/veilbridge-os/veilbridge/internal/vpn"
	"github.com/veilbridge-os/veilbridge/internal/vpn/amneziawg"
)

// New builds the OpenWrt adapter backed by the config store at the given path
// (config.DefaultPath if empty).
//
// The tunnel engine is chosen by asking the kernel, not by assuming (D-34,
// M1.7): see chooseEngine.
func New(configPath string) core.Adapter {
	engine, _ := chooseEngine(newSysProbe())
	return NewWithEngine(configPath, engine, "openwrt")
}

// chooseEngine picks the tunnel engine from what the device can actually do.
//
// The kernel engine is the one worth having: the tunnel shows up as a real
// interface (awg0) that the host's nftables forwards through transparently,
// which is what makes VeilBridge a router-wide gateway rather than a proxy for
// its own traffic. It needs /dev/net/tun, and on a board without kmod-tun that
// device node simply is not there.
//
// Not detecting means being wrong in one of two ways: assume kernel, and the
// daemon brings up no tunnel at all on a board without kmod-tun; assume
// userspace, and every router that could forward in the kernel pays for a
// userspace stack instead. Probing costs one stat and one open, once, at
// start up.
//
// The probe that answers here is the same one behind the kernel-tun
// capability, so the panel's "why" and the daemon's choice cannot drift
// apart: they are one measurement.
func chooseEngine(p sysProbe) (vpn.Engine, string) {
	if p.kernelTUN().Available {
		return amneziawg.NewKernelEngine(), core.TunnelEngineKernel
	}
	return amneziawg.NewNetstackEngine(), core.TunnelEngineUserspace
}
