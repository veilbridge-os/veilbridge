// Package mock provides in-memory implementations of the core manager
// interfaces. They let the API layer (Phase 6) be tested without a real OS,
// adapter, or VPN engine. Not for production use.
package mock

import (
	"fmt"
	"sync"
	"time"

	"github.com/veilbridge-os/veilbridge/internal/core"
)

// VPN is an in-memory core.VPNManager. Import* appends fabricated nodes;
// Activate just records which node is current. Safe for concurrent use.
type VPN struct {
	mu     sync.Mutex
	nodes  []core.Node
	active string // ID of the active node
}

func (m *VPN) ImportConfig(raw []byte) ([]core.Node, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := core.Node{
		ID:       fmt.Sprintf("node-%d", len(m.nodes)+1),
		Name:     fmt.Sprintf("imported-%d", len(m.nodes)+1),
		Engine:   core.EngineAmneziaWG,
		Endpoint: "203.0.113.1:443",
	}
	m.nodes = append(m.nodes, n)
	return []core.Node{n}, nil
}

func (m *VPN) ImportSubscription(url string) ([]core.Node, error) {
	return m.ImportConfig(nil)
}

func (m *VPN) ListNodes() ([]core.Node, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]core.Node, len(m.nodes))
	copy(out, m.nodes)
	return out, nil
}

func (m *VPN) RemoveNode(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, n := range m.nodes {
		if n.ID == id {
			m.nodes = append(m.nodes[:i], m.nodes[i+1:]...)
			if m.active == id {
				m.active = ""
			}
			return nil
		}
	}
	return fmt.Errorf("mock: node %q not found", id)
}

func (m *VPN) Activate(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, n := range m.nodes {
		if n.ID == id {
			m.active = id
			return nil
		}
	}
	return fmt.Errorf("mock: node %q not found", id)
}

func (m *VPN) Status(id string) (core.NodeStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, n := range m.nodes {
		if n.ID == id {
			return core.NodeStatus{
				NodeID:          id,
				Active:          m.active == id,
				HandshakeAgeSec: 5,
				RxBytes:         1024,
				TxBytes:         512,
			}, nil
		}
	}
	return core.NodeStatus{}, fmt.Errorf("mock: node %q not found", id)
}

// Routing is an in-memory core.RoutingManager. Apply is a no-op.
type Routing struct {
	mu    sync.Mutex
	rules []core.RouteRule
}

func (m *Routing) ListRules() ([]core.RouteRule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]core.RouteRule, len(m.rules))
	copy(out, m.rules)
	return out, nil
}

func (m *Routing) SetRules(rules []core.RouteRule) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rules = make([]core.RouteRule, len(rules))
	copy(m.rules, rules)
	return nil
}

func (m *Routing) Apply() error { return nil }

// System is a core.SystemManager returning canned data. ProbePath reports the
// expected path as the actual one (always OK) — enough to exercise the API.
type System struct{}

func (System) Info() (core.SystemInfo, error) {
	return core.SystemInfo{
		Platform:     "mock",
		Hostname:     "veilbridge-mock",
		Model:        "VeilBridge Demo Router",
		Firmware:     "OpenWrt 24.10.8",
		Kernel:       "6.6.144",
		UptimeSec:    3600,
		CPUPercent:   1.5,
		LoadAvg:      [3]float64{0.03, 0.05, 0.01},
		MemUsed:      100 << 20,
		MemTotal:     512 << 20,
		StorageUsed:  12 << 20,
		StorageTotal: 45 << 20,
		WANIP:        "198.51.100.1",
		EgressIP:     "203.0.113.1",
		EgressGeo:    "NL / Amsterdam",
		TunnelUp:     true,
	}, nil
}

func (System) ProbePath(target string, expected core.Target) (core.PathProbe, error) {
	return core.PathProbe{
		Target:      target,
		ExpectedVia: expected,
		ActualVia:   expected,
		OK:          true,
		Detail:      "mock probe",
	}, nil
}

func (System) Diagnostics(target string) (string, error) {
	return fmt.Sprintf("mock diagnostics for %s\n", target), nil
}

// Network returns a canned two-interface router: a static LAN and a DHCP WAN
// with a default route. Addresses are from the documentation ranges (RFC 5737)
// so a demo screenshot can never leak a real network.
type Network struct{}

func (Network) Interfaces() ([]core.NetworkInterface, error) {
	return []core.NetworkInterface{
		{
			Name: "lan", Device: "br-lan", Up: true, Proto: "static",
			UptimeSec: 3600, IPv4: []string{"192.168.1.1/24"},
		},
		{
			Name: "wan", Device: "eth1", Up: true, Proto: "dhcp",
			UptimeSec: 3540, IPv4: []string{"198.51.100.42/24"},
			Gateway: "198.51.100.1", DNS: []string{"198.51.100.1"},
		},
	}, nil
}

func (m Network) WANInfo() (core.WANStatus, error) {
	ifaces, _ := m.Interfaces()
	return core.WANStatus{
		Interface:  ifaces[1],
		SelectedBy: "default-route",
		Candidates: []string{"wan"},
	}, nil
}

// Device is a roadmap stub: every method returns core.ErrNotImplemented.
type Device struct{}

func (Device) ListDevices() ([]core.Device, error) {
	return nil, core.ErrNotImplemented
}

// Adapter bundles the mock managers into a core.Adapter.
type Adapter struct {
	vpn     *VPN
	routing *Routing
	system  System
	network Network
	device  Device
	applier *Applier
}

// NewAdapter returns a fully wired mock adapter.
func NewAdapter() *Adapter {
	return &Adapter{vpn: &VPN{}, routing: &Routing{}, applier: &Applier{}}
}

// Applier is an in-memory core.ConfigApplier: it counts what it was asked to
// do, so the API layer and demo mode can exercise the apply transaction with
// no OS underneath. Failures are injectable, because the paths worth testing
// are the ones where snapshot, commit or revert goes wrong.
type Applier struct {
	mu sync.Mutex

	Snapshots int
	Commits   int
	Reverts   int

	SnapshotErr error
	CommitErr   error
	RevertErr   error
}

func (a *Applier) Snapshot() (core.Snapshot, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Snapshots++
	if a.SnapshotErr != nil {
		return core.Snapshot{}, a.SnapshotErr
	}
	return core.Snapshot{
		ID:      fmt.Sprintf("mock-snap-%d", a.Snapshots),
		Taken:   time.Now(),
		Payload: []byte("mock-config"),
	}, nil
}

func (a *Applier) Commit() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Commits++
	return a.CommitErr
}

func (a *Applier) Revert(core.Snapshot) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Reverts++
	return a.RevertErr
}

// Seed pre-populates the manager with nodes and marks activeID as the current
// egress. It exists so a demo instance can show a populated UI without an OS,
// a router, or a real VPN endpoint.
func (m *VPN) Seed(activeID string, nodes ...core.Node) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nodes = append(m.nodes, nodes...)
	m.active = activeID
}

// NewDemoAdapter returns a mock adapter pre-loaded with plausible sample data.
// Every address it reports comes from the IANA documentation ranges (RFC 5737),
// so demo output is safe to publish as screenshots.
func NewDemoAdapter() *Adapter {
	a := NewAdapter()
	a.vpn.Seed("node-nl",
		core.Node{ID: "node-nl", Name: "amsterdam", Engine: core.EngineAmneziaWG, Endpoint: "203.0.113.10:443"},
		core.Node{ID: "node-fi", Name: "helsinki", Engine: core.EngineAmneziaWG, Endpoint: "203.0.113.20:51820"},
	)
	_ = a.routing.SetRules([]core.RouteRule{
		{ID: "r1", Kind: core.RuleDomain, Value: "example.com", Target: core.TargetTunnel},
		{ID: "r2", Kind: core.RuleSubnet, Value: "198.51.100.0/24", Target: core.TargetDirect},
	})
	return a
}

func (*Adapter) Platform() string { return "mock" }

// Capabilities describes a plausible device: a router with radios and a
// switch but no USB port. The demo screenshots are taken from this, so it has
// to look like hardware somebody owns - including one capability that is off,
// because a UI that never renders a disabled section is a UI nobody tested.
func (*Adapter) Capabilities() core.Capabilities {
	return core.Capabilities{
		core.CapWiFi:        {Available: true},
		core.CapSwitchPorts: {Available: true},
		core.CapKernelTUN:   {Available: true},
		core.CapIPv6:        {Available: true},
		core.CapUSB:         {Reason: "no USB controller on this device"},
	}
}
func (a *Adapter) VPN() core.VPNManager         { return a.vpn }
func (a *Adapter) Routing() core.RoutingManager { return a.routing }
func (a *Adapter) System() core.SystemManager   { return a.system }
func (a *Adapter) Network() core.NetworkManager { return a.network }
func (a *Adapter) Device() core.DeviceManager   { return a.device }
func (a *Adapter) Applier() core.ConfigApplier  { return a.applier }

// Compile-time guarantees that the mocks satisfy the core interfaces.
var (
	_ core.VPNManager      = (*VPN)(nil)
	_ core.RoutingManager  = (*Routing)(nil)
	_ core.SystemManager   = System{}
	_ core.NetworkManager  = Network{}
	_ core.DeviceManager   = Device{}
	_ core.Adapter         = (*Adapter)(nil)
	_ core.CapabilityProbe = (*Adapter)(nil)
)
