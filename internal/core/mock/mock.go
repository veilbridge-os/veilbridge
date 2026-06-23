// Package mock provides in-memory implementations of the core manager
// interfaces. They let the API layer (Phase 6) be tested without a real OS,
// adapter, or VPN engine. Not for production use.
package mock

import (
	"fmt"
	"sync"

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
		Platform:   "mock",
		Hostname:   "veilbridge-mock",
		UptimeSec:  3600,
		CPUPercent: 1.5,
		MemUsed:    100 << 20,
		MemTotal:   512 << 20,
		WANIP:      "198.51.100.1",
		EgressIP:   "203.0.113.1",
		EgressGeo:  "NL / Amsterdam",
		TunnelUp:   true,
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

// Network is a roadmap stub: every method returns core.ErrNotImplemented.
type Network struct{}

func (Network) WANInfo() (core.SystemInfo, error) {
	return core.SystemInfo{}, core.ErrNotImplemented
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
}

// NewAdapter returns a fully wired mock adapter.
func NewAdapter() *Adapter {
	return &Adapter{vpn: &VPN{}, routing: &Routing{}}
}

func (*Adapter) Platform() string               { return "mock" }
func (a *Adapter) VPN() core.VPNManager         { return a.vpn }
func (a *Adapter) Routing() core.RoutingManager { return a.routing }
func (a *Adapter) System() core.SystemManager   { return a.system }
func (a *Adapter) Network() core.NetworkManager { return a.network }
func (a *Adapter) Device() core.DeviceManager   { return a.device }

// Compile-time guarantees that the mocks satisfy the core interfaces.
var (
	_ core.VPNManager     = (*VPN)(nil)
	_ core.RoutingManager = (*Routing)(nil)
	_ core.SystemManager  = System{}
	_ core.NetworkManager = Network{}
	_ core.DeviceManager  = Device{}
	_ core.Adapter        = (*Adapter)(nil)
)
