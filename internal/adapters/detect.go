// Package adapters selects and constructs the per-platform core.Adapter at
// runtime. The API layer depends only on core interfaces; this is the one place
// that knows which concrete adapter to build. See the architecture notes in CONTRIBUTING.md
package adapters

import (
	"errors"
	"fmt"
	"os"

	"github.com/veilbridge-os/veilbridge/internal/adapters/openwrt"
	"github.com/veilbridge-os/veilbridge/internal/core"
)

// Platform identifies the detected host platform.
type Platform string

const (
	PlatformOpenWrt Platform = "openwrt"
	// PlatformUnsupported is every host that is not OpenWrt. VeilBridge manages
	// a router: it owns the firewall, the routing tables and (later) DHCP and
	// Wi-Fi. Running that logic on a host whose network is managed by something
	// else is how you get a half-applied config and a locked-out admin, so the
	// daemon refuses instead of guessing.
	PlatformUnsupported Platform = "unsupported"
)

// ErrUnsupportedPlatform is returned by New when the host is not OpenWrt.
var ErrUnsupportedPlatform = errors.New("adapters: unsupported platform")

// DetectPlatform reports the host platform: OpenWrt if /etc/openwrt_release
// exists, otherwise unsupported.
func DetectPlatform() Platform {
	if _, err := os.Stat("/etc/openwrt_release"); err == nil {
		return PlatformOpenWrt
	}
	return PlatformUnsupported
}

// New constructs the adapter for the detected platform, backed by the config
// store at configPath (config.DefaultPath if empty). It fails on anything that
// is not OpenWrt — the error names the way to look at the panel anyway (-demo),
// because a wrong-platform start is a user mistake, not a crash.
func New(configPath string) (core.Adapter, error) {
	switch DetectPlatform() {
	case PlatformOpenWrt:
		return openwrt.New(configPath), nil
	default:
		return nil, fmt.Errorf(
			"%w: /etc/openwrt_release not found. VeilBridge manages an OpenWrt "+
				"router; to look at the panel on this host, run with -demo",
			ErrUnsupportedPlatform,
		)
	}
}
