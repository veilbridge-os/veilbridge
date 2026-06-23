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
	script, err := routing.Generator{TunnelIface: "awg0"}.Render(rules)
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
	script, err := routing.Generator{}.Render(rules)
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
	script, err := routing.Generator{}.Render(nil)
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
	a, _ := routing.Generator{}.Render(rules)
	// Reversed input must yield identical output (sorted internally).
	b, _ := routing.Generator{}.Render([]core.RouteRule{rules[1], rules[0]})
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
			if _, err := (routing.Generator{}).Render([]core.RouteRule{r}); err == nil {
				t.Errorf("expected error for %q", name)
			}
		})
	}
}
