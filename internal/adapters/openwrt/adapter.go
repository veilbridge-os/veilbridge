// Package openwrt implements the core managers for OpenWrt hosts. It uses the
// kernel AmneziaWG engine (transparent nftables forwarding, no userspace Dialer)
// and ships VeilBridge's own routing — it does NOT depend on podkop.
//
// The manager wiring (config store, nft routing, /proc system info) is identical
// to Ubuntu's; only the VPN engine and the platform label differ. So the adapter
// is the Ubuntu adapter built with the kernel engine — this is the concrete proof
// of NFR-1 (the same code runs on both platforms, the adapter just picks the
// engine). See CONTRIBUTING.md §5 and the project history Phase 8.
package openwrt

import (
	"github.com/veilbridge-os/veilbridge/internal/adapters/ubuntu"
	"github.com/veilbridge-os/veilbridge/internal/core"
	"github.com/veilbridge-os/veilbridge/internal/vpn/amneziawg"
)

// New builds the OpenWrt adapter: the shared manager wiring with the kernel
// engine and the "openwrt" platform label.
func New(configPath string) core.Adapter {
	return ubuntu.NewWithEngine(configPath, amneziawg.NewKernelEngine(), "openwrt")
}
