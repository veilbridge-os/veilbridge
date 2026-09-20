package openwrt

import (
	"bufio"
	"strings"

	"github.com/veilbridge-os/veilbridge/internal/config"
	"github.com/veilbridge-os/veilbridge/internal/core"
	"github.com/veilbridge-os/veilbridge/internal/vpn"
)

// Adapter is the OpenWrt implementation of core.Adapter. It wires an AmneziaWG
// engine, nftables routing, and /proc system info together, all backed by one
// config store.
//
// The wiring is deliberately engine-agnostic: the tunnel engine is injected,
// not hardcoded. Devices differ in what they can run (a kernel TUN needs
// kmod-tun; the userspace netstack engine does not), so engine choice is a
// per-device decision, not a per-OS one — see NewWithEngine.
type Adapter struct {
	vpn      *vpnManager
	routing  *routingManager
	system   *systemManager
	network  networkManager
	device   deviceManager
	applier  *uciApplier
	platform string
}

// NewWithEngine builds the adapter with an explicit engine and platform label.
// New() is the product path (kernel engine); this constructor exists for the
// userspace-netstack fallback on devices without kmod-tun and for tests, which
// inject a fake engine to exercise manager logic offline.
func NewWithEngine(configPath string, engine vpn.Engine, platform string) *Adapter {
	store := config.NewStore(configPath)
	v := newVPNManager(store, engine)
	s := newSystemManager(v)
	s.platform = platform
	return &Adapter{
		vpn:      v,
		routing:  newRoutingManager(store),
		system:   s,
		applier:  newUCIApplier(),
		platform: platform,
	}
}

func (a *Adapter) Platform() string             { return a.platform }
func (a *Adapter) VPN() core.VPNManager         { return a.vpn }
func (a *Adapter) Routing() core.RoutingManager { return a.routing }
func (a *Adapter) System() core.SystemManager   { return a.system }
func (a *Adapter) Network() core.NetworkManager { return a.network }
func (a *Adapter) Device() core.DeviceManager   { return a.device }

// Applier exposes the uci-backed transaction. See uci.go for why a snapshot is
// a tarball of /etc/config and not a `uci export`.
func (a *Adapter) Applier() core.ConfigApplier { return a.applier }

// networkManager / deviceManager are roadmap stubs (M1/M3 and M4).
type networkManager struct{}

func (networkManager) WANInfo() (core.SystemInfo, error) {
	return core.SystemInfo{}, core.ErrNotImplemented
}

type deviceManager struct{}

func (deviceManager) ListDevices() ([]core.Device, error) {
	return nil, core.ErrNotImplemented
}

// defaultName derives a node name from a .conf. AmneziaWG configs carry no name,
// so we look for a leading "# Name = ..." comment (some panels emit one); failing
// that, the parser's stable ID-derived name is used by callers. Returns "" to let
// the parser/caller decide when nothing useful is found.
func defaultName(raw []byte) string {
	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "#") {
			continue
		}
		body := strings.TrimSpace(strings.TrimPrefix(line, "#"))
		if k, v, ok := strings.Cut(body, "="); ok && strings.EqualFold(strings.TrimSpace(k), "name") {
			if name := strings.TrimSpace(v); name != "" {
				return name
			}
		}
	}
	return "amneziawg-node"
}

// Compile-time guarantees.
var (
	_ core.Adapter        = (*Adapter)(nil)
	_ core.NetworkManager = networkManager{}
	_ core.DeviceManager  = deviceManager{}
)
