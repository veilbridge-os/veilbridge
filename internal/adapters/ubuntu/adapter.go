package ubuntu

import (
	"bufio"
	"strings"

	"github.com/veilbridge-os/veilbridge/internal/config"
	"github.com/veilbridge-os/veilbridge/internal/core"
	"github.com/veilbridge-os/veilbridge/internal/vpn/amneziawg"
)

// Adapter is the Ubuntu/Debian implementation of core.Adapter. It wires the
// userspace AmneziaWG engine, nftables routing, and /proc system info together,
// all backed by one config store.
type Adapter struct {
	vpn     *vpnManager
	routing *routingManager
	system  *systemManager
	network networkManager
	device  deviceManager
}

// New builds the Ubuntu adapter backed by the config store at the given path
// (config.DefaultPath if empty). The active engine is the userspace netstack
// engine (proven in Phase 4); the kernel engine is OpenWrt's adapter (Phase 8).
func New(configPath string) *Adapter {
	store := config.NewStore(configPath)
	v := newVPNManager(store, amneziawg.NewNetstackEngine())
	return &Adapter{
		vpn:     v,
		routing: newRoutingManager(store),
		system:  newSystemManager(v),
	}
}

func (*Adapter) Platform() string               { return "ubuntu" }
func (a *Adapter) VPN() core.VPNManager         { return a.vpn }
func (a *Adapter) Routing() core.RoutingManager { return a.routing }
func (a *Adapter) System() core.SystemManager   { return a.system }
func (a *Adapter) Network() core.NetworkManager { return a.network }
func (a *Adapter) Device() core.DeviceManager   { return a.device }

// networkManager / deviceManager are roadmap stubs (v0.2 / v0.4).
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
