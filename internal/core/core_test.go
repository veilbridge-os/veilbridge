package core_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/veilbridge-os/veilbridge/internal/core"
	"github.com/veilbridge-os/veilbridge/internal/core/mock"
)

// TestNodeJSONRoundTrip guards the wire contract: domain types must serialize
// with the field names the API and frontend expect.
func TestNodeJSONRoundTrip(t *testing.T) {
	in := core.Node{
		ID:       "abc",
		Name:     "NetherlandsAWG",
		Engine:   core.EngineAmneziaWG,
		Endpoint: "203.0.113.10:443",
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got, want := string(b), `{"id":"abc","name":"NetherlandsAWG","engine":"amneziawg","endpoint":"203.0.113.10:443"}`; got != want {
		t.Errorf("json mismatch:\n got %s\nwant %s", got, want)
	}
	var out core.Node
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out != in {
		t.Errorf("round-trip changed value: %+v != %+v", out, in)
	}
}

func TestRouteRuleEnums(t *testing.T) {
	r := core.RouteRule{Kind: core.RuleDomain, Target: core.TargetTunnel}
	if r.Kind != "domain" || r.Target != "tunnel" {
		t.Errorf("enum constants wrong: kind=%q target=%q", r.Kind, r.Target)
	}
}

func TestMockVPNLifecycle(t *testing.T) {
	a := mock.NewAdapter()
	vpn := a.VPN()

	nodes, err := vpn.ImportConfig([]byte("dummy"))
	if err != nil || len(nodes) != 1 {
		t.Fatalf("import: nodes=%d err=%v", len(nodes), err)
	}
	id := nodes[0].ID

	if err := vpn.Activate(id); err != nil {
		t.Fatalf("activate: %v", err)
	}
	st, err := vpn.Status(id)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !st.Active {
		t.Errorf("node %q should be active after Activate", id)
	}
	if st.HandshakeAgeSec < 0 {
		t.Errorf("expected a handshake age, got %d", st.HandshakeAgeSec)
	}

	if err := vpn.RemoveNode(id); err != nil {
		t.Fatalf("remove: %v", err)
	}
	all, _ := vpn.ListNodes()
	if len(all) != 0 {
		t.Errorf("expected no nodes after remove, got %d", len(all))
	}
}

func TestMockRouting(t *testing.T) {
	r := mock.NewAdapter().Routing()
	rules := []core.RouteRule{{ID: "r1", Kind: core.RuleSubnet, Value: "128.116.0.0/17", Target: core.TargetTunnel}}
	if err := r.SetRules(rules); err != nil {
		t.Fatalf("set rules: %v", err)
	}
	got, _ := r.ListRules()
	if len(got) != 1 || got[0].Value != "128.116.0.0/17" {
		t.Errorf("rules not stored: %+v", got)
	}
	if err := r.Apply(); err != nil {
		t.Errorf("apply: %v", err)
	}
}

// TestRoadmapStubs confirms the remaining stubs report ErrNotImplemented so the
// API layer can surface a clean "not available yet". Network left this list in
// M1.5; Device is still a stub until M4.
func TestRoadmapStubs(t *testing.T) {
	a := mock.NewAdapter()
	if _, err := a.Device().ListDevices(); !errors.Is(err, core.ErrNotImplemented) {
		t.Errorf("Device.ListDevices should return ErrNotImplemented, got %v", err)
	}
}

// TestMockNetwork: the demo adapter has to answer the network endpoints too,
// because the README screenshots are taken from it (and a demo that 501s on
// the dashboard's own calls is not a demo). The addresses must stay inside the
// documentation ranges so a screenshot can never leak a real network.
func TestMockNetwork(t *testing.T) {
	n := mock.NewAdapter().Network()

	ifaces, err := n.Interfaces()
	if err != nil || len(ifaces) == 0 {
		t.Fatalf("interfaces = %d, err = %v", len(ifaces), err)
	}
	wan, err := n.WANInfo()
	if err != nil {
		t.Fatalf("wan: %v", err)
	}
	if !wan.Interface.HasDefaultRoute() {
		t.Errorf("demo WAN has no default route: %+v", wan.Interface)
	}
	if wan.SelectedBy == "" {
		t.Error("demo WAN must say how it was selected")
	}
	for _, i := range ifaces {
		for _, addr := range i.IPv4 {
			if !strings.HasPrefix(addr, "198.51.100.") && !strings.HasPrefix(addr, "203.0.113.") &&
				!strings.HasPrefix(addr, "192.168.1.") {
				t.Errorf("demo address %q is outside the documentation ranges", addr)
			}
		}
	}
}
