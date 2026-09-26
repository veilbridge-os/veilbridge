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

// Vitals is the cheap read a sampler uses. The demo has nothing expensive to
// avoid, but it implements the interface anyway: otherwise the demo would
// exercise the fallback path and the product would exercise the real one,
// and the two would drift.
func (System) Vitals() (core.Vitals, error) {
	return core.Vitals{
		CPUPercent:  1.5,
		MemUsed:     100 << 20,
		MemTotal:    512 << 20,
		StorageUsed: 12 << 20,
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

// FirewallInfo describes the stock firewall of a home router plus one port
// forward and one rule of the owner's, so the screen has both kinds to show.
// Addresses are from the documentation ranges and the private LAN range.
func (m Network) FirewallInfo() (core.FirewallStatus, error) {
	return core.FirewallStatus{
		Zones: []core.FirewallZone{
			{Name: "lan", Role: core.ZoneLocal, Networks: []string{"lan"}, Live: true,
				Input: core.ActionAccept, Output: core.ActionAccept, Forward: core.ActionAccept},
			{Name: "wan", Role: core.ZoneInternet, Networks: []string{"wan", "wan6"}, Live: true,
				Input: core.ActionReject, Output: core.ActionAccept, Forward: core.ActionReject, Masquerade: true},
		},
		Forwardings: []core.ZoneForwarding{{From: "lan", To: "wan"}},
		PortForwards: []core.PortForward{
			{ID: "@redirect[0]", Name: "NAS", Enabled: true, Protocols: []string{"tcp"},
				From: "wan", ExternalPort: "8443", ToAddress: "192.168.1.50", ToPort: "443"},
		},
		Rules: []core.FirewallRule{
			{ID: "@rule[0]", Name: "Allow-DHCP-Renew", Enabled: true, System: true, From: "wan",
				Protocols: []string{"udp"}, Ports: "68", Action: core.ActionAccept, Family: "ipv4",
				Unsupported: []string{}},
			{ID: "@rule[1]", Name: "Allow-Ping", Enabled: true, System: true, From: "wan",
				Protocols: []string{"icmp"}, Action: core.ActionAccept, Family: "ipv4",
				Unsupported: []string{core.RuleICMPTypes}},
			{ID: "@rule[9]", Name: "Block game console", Enabled: true, From: "lan", To: "wan",
				Protocols: []string{"tcp", "udp"}, Action: core.ActionReject, Unsupported: []string{}},
			// A rule made in LuCI with a condition the panel does not show: the
			// screen has to name it, and offer only on/off and removal.
			{ID: "@rule[10]", Name: "Kids bedtime", Enabled: false, From: "lan", To: "wan",
				Protocols: []string{"tcp", "udp"}, Action: core.ActionReject,
				Unsupported: []string{core.RuleSchedule, core.RuleSourceAddress}},
		},
	}, nil
}

// StaticRoutes shows the three states the routes screen has to draw: a route
// the kernel is using, a switched-off one, and one that is on but not in the
// kernel (its gateway is not on the connection's network — D-75 refuses that
// today, but a route written by hand or by LuCI can still be like this).
func (m Network) StaticRoutes() (core.RoutesStatus, error) {
	return core.RoutesStatus{
		Routes: []core.StaticRoute{
			{ID: "@route[0]", Name: "Office", Enabled: true, Family: "ipv4",
				Target: "10.20.0.0/16", Gateway: "192.168.1.2", Interface: "lan",
				Active: true, Unsupported: []string{}},
			{ID: "@route[1]", Name: "Lab", Enabled: false, Family: "ipv4",
				Target: "172.16.5.0/24", Gateway: "192.168.1.3", Interface: "lan",
				Metric: 10, Unsupported: []string{}},
			{ID: "@route[2]", Enabled: true, Family: "ipv4",
				Target: "203.0.113.0/24", Gateway: "10.99.99.1", Interface: "wan",
				Unsupported: []string{}},
		},
		Interfaces: []core.RouteInterface{
			{Name: "lan", Up: true, IPv4: []string{"192.168.1.1/24"}},
			{Name: "wan", Up: true, IPv4: []string{"198.51.100.42/24"}},
		},
	}, nil
}

// LANInfo describes a plausible home network: a handout that is on, four
// clients and two addresses pinned by hand. It exists so the local-network
// screen can be seen and photographed without a router underneath — and so
// that the states worth looking at are present rather than convenient: one
// client announces no name, and one pinned device is not connected at all.
//
// Hardware addresses are locally administered (the `02:` prefix), which no
// vendor owns, so a demo screenshot cannot show anybody's real equipment.
//
// Writing is NOT implemented here, exactly as it is not for the uplink: the
// demo adapter reads, and the apply transaction belongs to a real device.
func (m Network) LANInfo() (core.LANStatus, error) {
	ifaces, _ := m.Interfaces()
	return core.LANStatus{
		Interface: ifaces[0],
		Handout: core.AddressHandout{
			Enabled: true, First: "192.168.1.100", Last: "192.168.1.249",
			LeaseSeconds: 43200,
		},
		Leases: []core.AddressLease{
			{MAC: "02:1a:a6:05:d4:9c", IP: "192.168.1.222", Hostname: "workshop-laptop", ExpiresSec: 42840},
			{MAC: "02:44:fd:18:0b:71", IP: "192.168.1.50", Hostname: "printer", ExpiresSec: 40100},
			{MAC: "02:0d:33:7a:55:c2", IP: "192.168.1.187", ExpiresSec: 30600},
			{MAC: "02:27:eb:4c:90:1e", IP: "192.168.1.60", Hostname: "storage", ExpiresSec: 12000},
		},
		Reserved: []core.ReservedAddress{
			{MAC: "02:44:fd:18:0b:71", IP: "192.168.1.50", Name: "printer", ID: "cfg01host"},
			{MAC: "02:27:eb:4c:90:1e", IP: "192.168.1.60", Name: "storage", ID: "cfg02host"},
		},
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
		core.CapDHCPServer:  {Available: true},
		core.CapUSB: {
			Reason: "this device has no USB port",
			Detail: "no USB controller on this board",
		},
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
	_ core.LANReader       = Network{}
	_ core.FirewallReader  = Network{}
	_ core.RouteReader     = Network{}
	_ core.DeviceManager   = Device{}
	_ core.Adapter         = (*Adapter)(nil)
	_ core.CapabilityProbe = (*Adapter)(nil)
)
