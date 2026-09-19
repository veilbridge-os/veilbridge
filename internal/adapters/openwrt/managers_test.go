package openwrt

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/veilbridge-os/veilbridge/internal/config"
	"github.com/veilbridge-os/veilbridge/internal/core"
	"github.com/veilbridge-os/veilbridge/internal/vpn"
)

// fakeEngine is a vpn.Engine that records up/down without real networking, so
// adapter logic (import/list/remove/activate bookkeeping) can be tested offline.
type fakeEngine struct {
	up        bool
	upErr     error
	handshake int
	dialer    vpn.Dialer
}

func (f *fakeEngine) Up(cfg vpn.NodeConfig) error {
	if f.upErr != nil {
		return f.upErr
	}
	f.up = true
	return nil
}
func (f *fakeEngine) Down() error { f.up = false; return nil }
func (f *fakeEngine) Stats() (vpn.Stats, error) {
	return vpn.Stats{HandshakeAgeSec: f.handshake, RxBytes: 10, TxBytes: 20}, nil
}
func (f *fakeEngine) Dialer() (vpn.Dialer, error) { return f.dialer, nil }

// fakeKernelEngine mimics a kernel engine: no Dialer, but a TUN interface name
// while up (satisfies vpn.InterfaceEngine). Lets us test the OpenWrt probe path
// — egress verified by interface, not Dialer — without root or a real tunnel.
type fakeKernelEngine struct{ up bool }

func (f *fakeKernelEngine) Up(vpn.NodeConfig) error { f.up = true; return nil }
func (f *fakeKernelEngine) Down() error             { f.up = false; return nil }
func (f *fakeKernelEngine) Stats() (vpn.Stats, error) {
	return vpn.Stats{HandshakeAgeSec: 1, RxBytes: 10, TxBytes: 20}, nil
}
func (f *fakeKernelEngine) Dialer() (vpn.Dialer, error) { return nil, nil }
func (f *fakeKernelEngine) TunName() string {
	if !f.up {
		return ""
	}
	return "awg0"
}

// stubDialer is a non-nil vpn.Dialer; it never actually dials (the egress func
// is stubbed in tests, so DialContext is never called).
type stubDialer struct{}

func (stubDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return nil, context.Canceled
}

const sampleConf = `[Interface]
PrivateKey = aGVsbG9wcml2YXRla2V5ZXhhbXBsZWJhc2U2NHN0cmluZw==
Address = 10.8.1.4/32
DNS = 1.1.1.1
Jc = 4

[Peer]
PublicKey = c2VydmVycHVibGlja2V5ZXhhbXBsZWJhc2U2NHN0cmluZ2hlcmU=
Endpoint = 203.0.113.20:51820
AllowedIPs = 0.0.0.0/0
`

func newTestVPN(t *testing.T, eng vpn.Engine) (*vpnManager, *config.Store) {
	t.Helper()
	store := config.NewStore(filepath.Join(t.TempDir(), "config.json"))
	return newVPNManager(store, eng), store
}

func TestVPNImportListRemove(t *testing.T) {
	m, _ := newTestVPN(t, &fakeEngine{})

	nodes, err := m.ImportConfig([]byte(sampleConf))
	if err != nil || len(nodes) != 1 {
		t.Fatalf("import: nodes=%d err=%v", len(nodes), err)
	}
	id := nodes[0].ID

	// Re-import of the same conf must NOT duplicate (stable ID from peer key).
	if _, err := m.ImportConfig([]byte(sampleConf)); err != nil {
		t.Fatalf("re-import: %v", err)
	}
	all, _ := m.ListNodes()
	if len(all) != 1 {
		t.Errorf("re-import duplicated node: %d nodes", len(all))
	}

	if err := m.RemoveNode(id); err != nil {
		t.Fatalf("remove: %v", err)
	}
	all, _ = m.ListNodes()
	if len(all) != 0 {
		t.Errorf("expected 0 nodes after remove, got %d", len(all))
	}
}

func TestVPNActivateBookkeeping(t *testing.T) {
	eng := &fakeEngine{handshake: 3}
	m, store := newTestVPN(t, eng)
	nodes, _ := m.ImportConfig([]byte(sampleConf))
	id := nodes[0].ID

	if err := m.Activate(id); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if !eng.up {
		t.Error("engine should be up after Activate")
	}
	// Active node is persisted in settings.
	doc, _ := store.Load()
	if doc.Settings.ActiveNodeID != id {
		t.Errorf("active node not persisted: %q", doc.Settings.ActiveNodeID)
	}
	// Status reflects active + handshake from the engine.
	st, _ := m.Status(id)
	if !st.Active || st.HandshakeAgeSec != 3 {
		t.Errorf("status wrong: %+v", st)
	}

	// Removing the active node tears it down and clears the active setting.
	if err := m.RemoveNode(id); err != nil {
		t.Fatalf("remove active: %v", err)
	}
	if eng.up {
		t.Error("engine should be down after removing active node")
	}
	doc, _ = store.Load()
	if doc.Settings.ActiveNodeID != "" {
		t.Errorf("active node should be cleared, got %q", doc.Settings.ActiveNodeID)
	}
}

func TestRoutingManagerPersistAndValidate(t *testing.T) {
	store := config.NewStore(filepath.Join(t.TempDir(), "config.json"))
	r := newRoutingManager(store)

	good := []core.RouteRule{{ID: "1", Kind: core.RuleSubnet, Value: "10.0.0.0/8", Target: core.TargetTunnel}}
	if err := r.SetRules(good); err != nil {
		t.Fatalf("set good rules: %v", err)
	}
	got, _ := r.ListRules()
	if len(got) != 1 || got[0].Value != "10.0.0.0/8" {
		t.Errorf("rules not persisted: %+v", got)
	}

	// Bad CIDR is rejected at SetRules (validated by rendering), not persisted.
	bad := []core.RouteRule{{ID: "2", Kind: core.RuleSubnet, Value: "not-a-cidr", Target: core.TargetTunnel}}
	if err := r.SetRules(bad); err == nil {
		t.Error("expected SetRules to reject a bad CIDR")
	}
	got, _ = r.ListRules()
	if len(got) != 1 {
		t.Errorf("bad rules should not have replaced good ones: %+v", got)
	}
}

func TestProbePathNoTunnel(t *testing.T) {
	m, _ := newTestVPN(t, &fakeEngine{})
	sys := newSystemManager(m)
	sys.egress = func(func(*http.Request) (*http.Response, error)) string { return "198.51.100.1" }
	// No active tunnel → everything is direct; expecting direct → OK.
	probe, err := sys.ProbePath("example.com", core.TargetDirect)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if probe.ActualVia != core.TargetDirect || !probe.OK {
		t.Errorf("no-tunnel probe wrong: %+v", probe)
	}
}

func TestProbePathThroughTunnel(t *testing.T) {
	eng := &fakeEngine{dialer: stubDialer{}}
	m, _ := newTestVPN(t, eng)
	nodes, _ := m.ImportConfig([]byte(sampleConf))
	if err := m.Activate(nodes[0].ID); err != nil {
		t.Fatalf("activate: %v", err)
	}
	sys := newSystemManager(m)
	// Direct egress vs tunnel egress differ → traffic took the tunnel.
	calls := 0
	sys.egress = func(func(*http.Request) (*http.Response, error)) string {
		calls++
		if calls == 1 {
			return "198.51.100.1" // direct
		}
		return "203.0.113.20" // tunnel
	}
	probe, err := sys.ProbePath("youtube.com", core.TargetTunnel)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if probe.ActualVia != core.TargetTunnel || !probe.OK {
		t.Errorf("tunnel probe wrong: %+v", probe)
	}
}

// TestProbePathKernelTunnel: the OpenWrt path — a kernel engine has no Dialer,
// so the probe must verify egress via the TUN interface and still report tunnel.
func TestProbePathKernelTunnel(t *testing.T) {
	eng := &fakeKernelEngine{}
	m, _ := newTestVPN(t, eng)
	nodes, _ := m.ImportConfig([]byte(sampleConf))
	if err := m.Activate(nodes[0].ID); err != nil {
		t.Fatalf("activate: %v", err)
	}
	sys := newSystemManager(m)
	calls := 0
	sys.egress = func(func(*http.Request) (*http.Response, error)) string {
		calls++
		if calls == 1 {
			return "198.51.100.1" // direct WAN
		}
		return "203.0.113.20" // via awg0
	}
	probe, err := sys.ProbePath("youtube.com", core.TargetTunnel)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if probe.ActualVia != core.TargetTunnel || !probe.OK {
		t.Errorf("kernel tunnel probe wrong: %+v", probe)
	}
}

func TestValidHost(t *testing.T) {
	ok := []string{"1.1.1.1", "example.com", "sub-domain.example.io", "2001:db8::1"}
	bad := []string{"", "a b", "x;rm -rf", "$(whoami)", "a/b"}
	for _, s := range ok {
		if !validHost(s) {
			t.Errorf("validHost(%q) = false, want true", s)
		}
	}
	for _, s := range bad {
		if validHost(s) {
			t.Errorf("validHost(%q) = true, want false", s)
		}
	}
}
