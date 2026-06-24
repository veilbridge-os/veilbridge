// Package adapters selects and constructs the per-platform core.Adapter at
// runtime. The API layer depends only on core interfaces; this is the one place
// that knows which concrete adapter to build. See CONTRIBUTING.md §1, §4.
package adapters

import (
	"os"

	"github.com/veilbridge-os/veilbridge/internal/adapters/openwrt"
	"github.com/veilbridge-os/veilbridge/internal/adapters/ubuntu"
	"github.com/veilbridge-os/veilbridge/internal/core"
)

// Platform identifies the detected host platform.
type Platform string

const (
	PlatformOpenWrt Platform = "openwrt"
	PlatformUbuntu  Platform = "ubuntu"
)

// DetectPlatform reports the host platform: OpenWrt if /etc/openwrt_release
// exists, otherwise Ubuntu/Debian (the default).
func DetectPlatform() Platform {
	if _, err := os.Stat("/etc/openwrt_release"); err == nil {
		return PlatformOpenWrt
	}
	return PlatformUbuntu
}

// New constructs the adapter for the detected platform, backed by the config
// store at configPath (config.DefaultPath if empty). OpenWrt gets the kernel
// engine (transparent nft forwarding); everything else the userspace netstack
// engine. The detected platform is reported by SystemManager.Info.
func New(configPath string) (core.Adapter, error) {
	switch DetectPlatform() {
	case PlatformOpenWrt:
		return openwrt.New(configPath), nil
	default:
		return ubuntu.New(configPath), nil
	}
}
