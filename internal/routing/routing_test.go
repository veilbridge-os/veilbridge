package routing_test

import (
	"strings"
	"testing"

	"github.com/veilbridge-os/veilbridge/internal/core"
	"github.com/veilbridge-os/veilbridge/internal/routing"
)

func TestRenderSubnets(t *testing.T) {
	rules := []core.RouteRule{
		{ID: "1", Kind: core.RuleSubnet, Value: "128.116.0.0/17", Target: core.TargetTunnel},
		{ID: "2", Kind: core.RuleSubnet, Value: "185.73.192.0/22", Target: core.TargetDirect},
		{ID: "3", Kind: core.RuleSubnet, Value: "2001:db8::/32", Target: core.TargetTunnel},
	}
	script, err := routing.Generator{Ingress: []string{"br-lan"}}.Render(rules)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// Idempotent reset present.
	if !strings.Contains(script, "add table inet veilbridge") ||
		!strings.Contains(script, "delete table inet veilbridge") {
		t.Errorf("missing idempotent reset:\n%s", script)
	}
	// Tunnel v4 subnet in tunnel4 set + marked.
	if !strings.Contains(script, "128.116.0.0/17") {
		t.Errorf("tunnel v4 subnet missing")
	}
	if !strings.Contains(script, "ip daddr @tunnel4 meta mark set") {
		t.Errorf("tunnel mark rule missing:\n%s", script)
	}
	// Direct subnet returns before any tunnel mark (precedence).
	if !strings.Contains(script, "ip daddr @direct4 return") {
		t.Errorf("direct return rule missing")
	}
	if idxDirect, idxMark := strings.Index(script, "@direct4 return"), strings.Index(script, "@tunnel4 meta mark"); idxDirect > idxMark {
		t.Errorf("direct rule must come before tunnel mark (precedence)")
	}
	// v6 tunnel subnet handled.
	if !strings.Contains(script, "2001:db8::/32") || !strings.Contains(script, "ip6 daddr @tunnel6") {
		t.Errorf("v6 tunnel handling missing:\n%s", script)
	}
}

func TestRenderDomainsAreCommentsInV01(t *testing.T) {
	rules := []core.RouteRule{
		{ID: "1", Kind: core.RuleDomain, Value: "youtube.com", Target: core.TargetTunnel},
	}
	script, err := routing.Generator{Ingress: []string{"br-lan"}}.Render(rules)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	// Domain rules are not yet enforced (v0.1) — emitted as comments only.
	if !strings.Contains(script, "# domain rules") || !strings.Contains(script, "youtube.com") {
		t.Errorf("domain rule should appear as a comment:\n%s", script)
	}
	if strings.Contains(script, "youtube.com\n") && strings.Contains(script, "meta mark") && !strings.Contains(script, "#") {
		t.Errorf("domain must NOT produce an enforced rule in v0.1")
	}
}

func TestRenderEmpty(t *testing.T) {
	script, err := routing.Generator{Ingress: []string{"br-lan"}}.Render(nil)
	if err != nil {
		t.Fatalf("render empty: %v", err)
	}
	// Empty rule set still produces a valid, complete table definition.
	if !strings.Contains(script, "table inet veilbridge {") || !strings.Contains(script, "chain prerouting") {
		t.Errorf("empty render should still define the table+chain:\n%s", script)
	}
}

func TestRenderDeterministic(t *testing.T) {
	rules := []core.RouteRule{
		{ID: "1", Kind: core.RuleSubnet, Value: "10.0.0.0/8", Target: core.TargetTunnel},
		{ID: "2", Kind: core.RuleSubnet, Value: "10.1.0.0/16", Target: core.TargetTunnel},
	}
	a, _ := routing.Generator{Ingress: []string{"br-lan"}}.Render(rules)
	// Reversed input must yield identical output (sorted internally).
	b, _ := routing.Generator{Ingress: []string{"br-lan"}}.Render([]core.RouteRule{rules[1], rules[0]})
	if a != b {
		t.Errorf("render not deterministic:\n--- a ---\n%s\n--- b ---\n%s", a, b)
	}
}

func TestRenderErrors(t *testing.T) {
	cases := map[string]core.RouteRule{
		"bad cidr":     {Kind: core.RuleSubnet, Value: "not-a-cidr"},
		"empty domain": {Kind: core.RuleDomain, Value: "  "},
		"unknown kind": {Kind: "bogus", Value: "x"},
	}
	for name, r := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := (routing.Generator{Ingress: []string{"br-lan"}}).Render([]core.RouteRule{r}); err == nil {
				t.Errorf("expected error for %q", name)
			}
		})
	}
}

// The tunnel mark is one bit set with OR, so marks other packages put on the
// same packets survive (D-73), and the adapter's rule matches it under a mask.
func TestRenderSetsTunnelMarkAsOneBit(t *testing.T) {
	script, err := routing.Generator{Ingress: []string{"br-lan"}}.Render([]core.RouteRule{
		{Kind: core.RuleSubnet, Value: "198.51.100.0/24", Target: core.TargetTunnel},
		{Kind: core.RuleSubnet, Value: "2001:db8::/32", Target: core.TargetTunnel},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ip daddr @tunnel4 meta mark set meta mark | 0x40000000",
		"ip6 daddr @tunnel6 meta mark set meta mark | 0x40000000",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("missing %q in:\n%s", want, script)
		}
	}
}

// Only traffic from the local network is marked: a reply out of the tunnel, a
// port forwarded from the uplink or a guest would otherwise match a broad
// rule and be sent back into the tunnel (#47).
func TestRenderMarksOnlyTrafficFromTheLocalNetwork(t *testing.T) {
	rules := []core.RouteRule{{Kind: core.RuleSubnet, Value: "0.0.0.0/0", Target: core.TargetTunnel}}
	script, err := routing.Generator{Ingress: []string{"br-lan", "lan2"}}.Render(rules)
	if err != nil {
		t.Fatal(err)
	}
	ret := strings.Index(script, `iifname != { "br-lan", "lan2" } return`)
	reply := strings.Index(script, "ct direction reply return")
	mark := strings.Index(script, "meta mark set")
	if ret < 0 || reply < 0 || mark < 0 || ret > mark || reply > mark {
		t.Errorf("the ingress and reply checks must come before any mark:\n%s", script)
	}
}

func TestRenderRefusesWithoutTheLocalNetwork(t *testing.T) {
	rules := []core.RouteRule{{Kind: core.RuleSubnet, Value: "198.51.100.0/24", Target: core.TargetTunnel}}
	if _, err := (routing.Generator{}).Render(rules); err == nil {
		t.Error("no ingress must be refused, not rendered as \"mark everything\"")
	}
	for _, bad := range []string{"", `br"lan`, "a b", "x;y", "averyveryverylongname"} {
		if _, err := (routing.Generator{Ingress: []string{bad}}).Render(rules); err == nil {
			t.Errorf("interface name %q accepted", bad)
		}
	}
}
