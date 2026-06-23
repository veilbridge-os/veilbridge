// Package adapters selects and constructs the per-platform core.Adapter at
// runtime. The API layer depends only on core interfaces; this is the one place
// that knows which concrete adapter to build. See CONTRIBUTING.md §1, §4.
package adapters

import (
	"os"

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
// store at configPath (config.DefaultPath if empty).
//
// OpenWrt's adapter (kernel engine) lands in Phase 8; until then OpenWrt hosts
// fall through to the Ubuntu adapter, which is wrong for transparent forwarding
// but lets the agent at least run. The detected platform is reported by
// SystemManager.Info so the UI shows the truth.
func New(configPath string) (core.Adapter, error) {
	switch DetectPlatform() {
	case PlatformOpenWrt:
		// TODO(Phase 8): return openwrt.New(configPath)
		return ubuntu.New(configPath), nil
	default:
		return ubuntu.New(configPath), nil
	}
}
