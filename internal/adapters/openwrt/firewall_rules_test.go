package openwrt

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/veilbridge-os/veilbridge/internal/core"
)

// The router's own firewall, captured from it: nine stock rules and, at
// @rule[9], the owner's rule that keeps the panel reachable from the internet
// side (tcp 22 and 8080 from wan, ACCEPT). Section 13 of the file.
const routerFirewall = "firewall-25.12.5.txt"

func blockFromWan(port string) core.FirewallRuleConfig {
	return core.FirewallRuleConfig{
		Name: "No panel from outside", Enabled: true, From: "wan",
		Protocols: []string{"tcp"}, Ports: port, Action: core.ActionReject,
	}
}

func TestANewRuleIsStagedAsOneRowNamedByWhatItDoes(t *testing.T) {
	m, f := firewallWriter(t, fixture(t, routerFirewall))
	cs, err := m.StageRule(core.FirewallRuleConfig{
		Name: "Game console", Enabled: true, From: "lan", To: "wan",
		Protocols: []string{"udp", "tcp"}, Ports: "3074, 3478-3480", Action: core.ActionDrop,
	})
	if err != nil {
		t.Fatal(err)
	}
	staged := strings.Join(f.uci.sets(), "|")
	for _, want := range []string{
		"firewall.cfg05fe63.src=lan", "firewall.cfg05fe63.dest=wan",
		"firewall.cfg05fe63.proto=tcp udp", "firewall.cfg05fe63.dest_port=3074 3478-3480",
		"firewall.cfg05fe63.target=DROP", "firewall.cfg05fe63.name=Game console",
	} {
		if !strings.Contains(staged, want) {
			t.Errorf("staged %q, want it to contain %q", staged, want)
		}
	}
	if len(cs) != 1 || cs[0].LabelKey != "firewall.rule.dropForward" ||
		cs[0].To != "lan → wan · tcp+udp 3074, 3478-3480" || !cs[0].Dangerous {
		t.Errorf("rows = %+v, want one dangerous row in words", cs)
	}
	if f.called("uci commit") {
		t.Fatal("a rule was committed instead of staged")
	}
	if !f.called(fw4Program + " check") {
		t.Error("the firewall was never asked whether it accepts the draft (D-66)")
	}
	if f.called("uci reorder") {
		t.Error("a rule with no place asked for was moved")
	}
}

// D-71, measured on the router: the owner's rule allowing 22 and 8080 from the
// internet side is above anything added later, so a block of 8080 appended
// after it would never act. That is refused, and it names the rule and the
// way out.
func TestARuleThatCouldNeverActIsRefusedWithTheRuleInTheWay(t *testing.T) {
	m, f := firewallWriter(t, fixture(t, routerFirewall))
	_, err := m.StageRule(blockFromWan("8080"))
	assertRefusedField(t, err, "before")
	if !strings.Contains(err.Error(), "VB-temp-wan-mgmt") {
		t.Errorf("err = %v, want it to name the rule in the way", err)
	}
	if f.called("uci add") || len(f.uci.sets()) != 0 {
		t.Errorf("the device was touched before refusing: %v", f.uci.calls)
	}
}

// Only certainty refuses. A port the earlier rule does not cover, a rule that
// is switched off, and one that agrees with it all go through.
func TestOnlyACertainShadowIsRefused(t *testing.T) {
	cases := map[string]core.FirewallRuleConfig{
		"another port":  blockFromWan("443"),
		"a wider range": blockFromWan("8000-8090"),
		"switched off":  func() core.FirewallRuleConfig { c := blockFromWan("8080"); c.Enabled = false; return c }(),
		"same outcome":  func() core.FirewallRuleConfig { c := blockFromWan("22"); c.Action = core.ActionAccept; return c }(),
		"another zone":  func() core.FirewallRuleConfig { c := blockFromWan("8080"); c.From = "lan"; return c }(),
		"udp":           func() core.FirewallRuleConfig { c := blockFromWan("8080"); c.Protocols = []string{"udp"}; return c }(),
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			m, _ := firewallWriter(t, fixture(t, routerFirewall))
			if _, err := m.StageRule(cfg); err != nil {
				t.Errorf("refused: %v", err)
			}
		})
	}
}

// A switched-off rule decides nothing, so it cannot be in anybody's way.
func TestASwitchedOffRuleDoesNotShadow(t *testing.T) {
	show := strings.Replace(fixture(t, routerFirewall),
		"firewall.@rule[9].target='ACCEPT'", "firewall.@rule[9].target='ACCEPT'\nfirewall.@rule[9].enabled='0'", 1)
	m, _ := firewallWriter(t, show)
	if _, err := m.StageRule(blockFromWan("8080")); err != nil {
		t.Errorf("refused behind a rule that is off: %v", err)
	}
}

// D-70: placed in front of the rule in the way, the same block is accepted
// and lands there — the index is the section's place in the whole file.
func TestANewRuleCanBePlacedInFrontOfAnother(t *testing.T) {
	m, f := firewallWriter(t, fixture(t, routerFirewall))
	cfg := blockFromWan("8080")
	cfg.Before = "@rule[9]"
	cs, err := m.StageRule(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !f.called("uci reorder firewall.cfg05fe63=13") {
		t.Errorf("calls = %v, want the new rule moved to section 13", f.uci.calls)
	}
	if len(cs) != 1 || cs[0].LabelKey != "firewall.rule.rejectInput" || cs[0].To != "wan · tcp 8080" {
		t.Errorf("rows = %+v", cs)
	}
}

func TestRuleRefusalsNameTheirField(t *testing.T) {
	ok := core.FirewallRuleConfig{Enabled: true, From: "lan", To: "wan", Protocols: []string{"tcp"}, Ports: "25", Action: core.ActionReject}
	cases := map[string]func(c *core.FirewallRuleConfig){
		"protocols/none":    func(c *core.FirewallRuleConfig) { c.Protocols = nil },
		"protocols/gre":     func(c *core.FirewallRuleConfig) { c.Protocols = []string{"gre"} },
		"protocols/all-and": func(c *core.FirewallRuleConfig) { c.Protocols = []string{"all", "tcp"}; c.Ports = "" },
		"ports/with-icmp":   func(c *core.FirewallRuleConfig) { c.Protocols = []string{"tcp", "icmp"} },
		"ports/too-big":     func(c *core.FirewallRuleConfig) { c.Ports = "99999" },
		"ports/word":        func(c *core.FirewallRuleConfig) { c.Ports = "smtp" },
		"ports/half-a-list": func(c *core.FirewallRuleConfig) { c.Ports = "25 x" },
		"action/allow":      func(c *core.FirewallRuleConfig) { c.Action = "allow" },
		"action/empty":      func(c *core.FirewallRuleConfig) { c.Action = "" },
		"family/ipv5":       func(c *core.FirewallRuleConfig) { c.Family = "ipv5" },
		"from/empty":        func(c *core.FirewallRuleConfig) { c.From = "" },
		"from/unknown":      func(c *core.FirewallRuleConfig) { c.From = "guest" },
		"from/not-a-name":   func(c *core.FirewallRuleConfig) { c.From = "lan;reboot" },
		"to/unknown":        func(c *core.FirewallRuleConfig) { c.To = "dmz" },
		"name/quote":        func(c *core.FirewallRuleConfig) { c.Name = "it's" },
		"id/unknown":        func(c *core.FirewallRuleConfig) { c.ID = "@rule[42]" },
		"id/not-an-entry":   func(c *core.FirewallRuleConfig) { c.ID = "wan;reboot" },
		"id/system":         func(c *core.FirewallRuleConfig) { c.ID = "@rule[0]" },
		"before/unknown":    func(c *core.FirewallRuleConfig) { c.Before = "@rule[42]" },
		"before/with-an-id": func(c *core.FirewallRuleConfig) { c.ID = "@rule[9]"; c.Before = "@rule[1]" },
	}
	for name, edit := range cases {
		field, _, _ := strings.Cut(name, "/")
		t.Run(name, func(t *testing.T) {
			m, f := firewallWriter(t, fixture(t, routerFirewall))
			cfg := ok
			edit(&cfg)
			_, err := m.StageRule(cfg)
			assertRefusedField(t, err, field)
			if len(f.uci.sets()) != 0 || f.called("uci add") {
				t.Errorf("the device was touched before refusing: %v", f.uci.calls)
			}
		})
	}
}

// D-67: the rules the firewall ships are read-only, removal included.
func TestAStockRuleCannotBeRemoved(t *testing.T) {
	m, f := firewallWriter(t, fixture(t, routerFirewall))
	_, err := m.RemoveRule("@rule[1]")
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Errorf("err = %v, want a refusal saying the rule is read-only", err)
	}
	if f.called("uci delete") {
		t.Error("a stock rule was deleted")
	}
}

func TestRemovingARuleIsOneRowNamedInWords(t *testing.T) {
	m, f := firewallWriter(t, fixture(t, routerFirewall))
	cs, err := m.RemoveRule("@rule[9]")
	if err != nil {
		t.Fatal(err)
	}
	if !f.called("uci delete firewall.@rule[9]") {
		t.Errorf("calls = %v", f.uci.calls)
	}
	if len(cs) != 1 || cs[0].LabelKey != "firewall.rule.acceptInput" || cs[0].From != "wan · tcp 22, 8080" || cs[0].To != "" {
		t.Errorf("rows = %+v", cs)
	}
	if _, err := m.RemoveRule("@rule[42]"); err == nil {
		t.Error("removing a rule that is not there was accepted")
	}
}

const showWithScheduledRule = `firewall.@zone[0]=zone
firewall.@zone[0].name='lan'
firewall.@zone[0].network='lan'
firewall.@zone[1]=zone
firewall.@zone[1].name='wan'
firewall.@zone[1].network='wan'
firewall.@zone[1].masq='1'
firewall.@rule[0]=rule
firewall.@rule[0].name='Kids bedtime'
firewall.@rule[0].src='lan'
firewall.@rule[0].dest='wan'
firewall.@rule[0].src_mac='02:00:00:00:00:07'
firewall.@rule[0].start_time='22:00:00'
firewall.@rule[0].target='REJECT'
`

// A rule with conditions the panel does not show can be switched off — and
// only that. Editing it here would change a rule whose meaning is partly
// invisible on this screen.
func TestARuleWithHiddenConditionsCanOnlyBeSwitched(t *testing.T) {
	kids := core.FirewallRuleConfig{
		ID: "@rule[0]", Name: "Kids bedtime", From: "lan", To: "wan",
		Protocols: []string{"tcp", "udp"}, Action: core.ActionReject,
	}
	m, f := firewallWriter(t, showWithScheduledRule)
	f.uci.values = map[string]string{
		"firewall.@rule[0].name": "Kids bedtime", "firewall.@rule[0].src": "lan",
		"firewall.@rule[0].dest": "wan", "firewall.@rule[0].target": "REJECT",
	}
	cs, err := m.StageRule(kids) // Enabled: false
	if err != nil {
		t.Fatalf("switching it off was refused: %v", err)
	}
	if len(cs) != 1 || cs[0].LabelKey != "firewall.rule.enabled" || cs[0].To != "0" || cs[0].Subject != "Kids bedtime" {
		t.Errorf("rows = %+v, want exactly one: this rule switched off", cs)
	}
	if f.called("uci set firewall.@rule[0].proto") {
		t.Error("an absent proto (TCP and UDP) was written out: a row that changes nothing")
	}

	m, f = firewallWriter(t, showWithScheduledRule)
	edited := kids
	edited.Ports = "443"
	_, err = m.StageRule(edited)
	assertRefusedField(t, err, "id")
	if !strings.Contains(err.Error(), "sourceAddress") || !strings.Contains(err.Error(), "schedule") {
		t.Errorf("err = %v, want it to name the hidden conditions", err)
	}
	if len(f.uci.sets()) != 0 {
		t.Errorf("the device was touched: %v", f.uci.calls)
	}
}

func TestReadingARuleNamesWhatThePanelDoesNotShow(t *testing.T) {
	st := parseFirewall(showWithScheduledRule+`firewall.@rule[1]=rule
firewall.@rule[1].src='lan'
firewall.@rule[1].target='MARK'
firewall.@rule[1].set_mark='0x10'
`, nil)
	if got := st.Rules[0].Unsupported; !slices.Equal(got, []string{core.RuleSchedule, core.RuleSourceAddress}) {
		t.Errorf("hidden conditions = %v", got)
	}
	// A MARK rule decides nothing; reading it as "accept" would be a lie on
	// the screen people open to find out what is allowed.
	if r := st.Rules[1]; r.Action != core.ActionOther || !slices.Contains(r.Unsupported, core.RuleOther) {
		t.Errorf("marking rule = %+v", r)
	}
	// And a stock rule with ICMP types reports them, not a clean bill.
	fw := parseFirewall(fixture(t, routerFirewall), nil)
	for _, r := range fw.Rules {
		if r.Name == "Allow-MLD" && !slices.Equal(r.Unsupported, []string{core.RuleICMPTypes, core.RuleSourceAddress}) {
			t.Errorf("Allow-MLD hidden conditions = %v", r.Unsupported)
		}
		if r.Name == "VB-temp-wan-mgmt" && len(r.Unsupported) != 0 {
			t.Errorf("the owner's plain rule reports hidden conditions: %v", r.Unsupported)
		}
	}
}

func TestADraftRuleTheFirewallWouldSkipIsDropped(t *testing.T) {
	skipped := "Ruleset passes nftables check.\n[!] Section @rule[10] (x) option 'dest_port' specifies invalid value\n"
	m, f := firewallWriter(t, fixture(t, routerFirewall), "Ruleset passes nftables check.\n", skipped)
	_, err := m.StageRule(blockFromWan("443"))
	if err == nil || !strings.Contains(err.Error(), "would skip") {
		t.Fatalf("err = %v, want a refusal quoting the firewall", err)
	}
	if !f.called("uci revert firewall") {
		t.Error("the refused draft was left on the device")
	}
}

// The apply bar reads the draft back from the device after a reload. A new
// rule and a removed one must read the same as when they were staged — and
// the removed one is where it broke: `uci changes` names it `cfg…`, the
// committed file prints it `@rule[…]`, and nothing asked for its fields.
func TestRulesReadBackAsTheyWereStaged(t *testing.T) {
	r := &recordingRunner{
		values: map[string]string{
			"firewall.cfg05fe63":           "rule",
			"firewall.cfg05fe63.src":       "wan",
			"firewall.cfg05fe63.proto":     "tcp",
			"firewall.cfg05fe63.dest_port": "8080",
			"firewall.cfg05fe63.target":    "REJECT",
		},
		committed: map[string]string{
			"firewall.cfg0192bd":           "rule",
			"firewall.cfg0192bd.src":       "lan",
			"firewall.cfg0192bd.dest":      "wan",
			"firewall.cfg0192bd.target":    "DROP",
			"firewall.cfg0292bd":           "redirect",
			"firewall.cfg0292bd.src":       "wan",
			"firewall.cfg0292bd.proto":     "tcp",
			"firewall.cfg0292bd.src_dport": "8443",
			"firewall.cfg0292bd.dest_ip":   "192.168.1.50",
			"firewall.cfg0292bd.dest_port": "443",
			"firewall.cfg0292bd.target":    "DNAT",
		},
		sectionType: "rule",
		anonymous: map[string]string{
			"cfg05fe63": "@rule[1]", "cfg0192bd": "@rule[0]", "cfg0292bd": "@redirect[0]",
		},
		stagedLines: []string{
			"firewall.cfg05fe63='rule'",
			"firewall.cfg05fe63.src='wan'",
			"firewall.cfg05fe63.proto='tcp'",
			"firewall.cfg05fe63.dest_port='8080'",
			"firewall.cfg05fe63.target='REJECT'",
			"firewall.cfg05fe63='13'", // the placement, measured shape
			"-firewall.cfg0192bd",
			"-firewall.cfg0292bd",
		},
	}
	cs, err := (networkManager{run: r.run, configDir: t.TempDir()}).StagedChanges()
	if err != nil {
		t.Fatal(err)
	}
	want := []core.ConfigChange{
		{LabelKey: "firewall.rule.rejectInput", To: "wan · tcp 8080"},
		{LabelKey: "firewall.rule.dropForward", From: "lan → wan · tcp+udp"}, // no proto is TCP and UDP
		{LabelKey: "firewall.redirect.section", From: "tcp 8443 → 192.168.1.50:443"},
	}
	if len(cs) != len(want) {
		t.Fatalf("rows = %+v, want %d", cs, len(want))
	}
	for i, w := range want {
		if cs[i].LabelKey != w.LabelKey || cs[i].From != w.From || cs[i].To != w.To {
			t.Errorf("row %d = %+v, want %+v", i, cs[i], w)
		}
	}
	assertKnownKeys(t, "rules read back", cs)
}

// A row about one field of one of several rules says which rule. Measured on
// the router: switching off the rule that keeps the panel reachable read
// "Rule is on: yes → no" beside a new rule, and nothing said which of the two
// was being switched off. Both paths must name it, and name it alike.
func TestAFieldRowNamesItsRuleOnBothPaths(t *testing.T) {
	m, f := firewallWriter(t, fixture(t, routerFirewall))
	f.uci.values = map[string]string{
		"firewall.@rule[9].name": "VB-temp-wan-mgmt", "firewall.@rule[9].src": "wan",
		"firewall.@rule[9].proto": "tcp", "firewall.@rule[9].dest_port": "22 8080", "firewall.@rule[9].target": "ACCEPT",
	}
	cs, err := m.StageRule(core.FirewallRuleConfig{
		ID: "@rule[9]", Name: "VB-temp-wan-mgmt", Enabled: false, From: "wan",
		Protocols: []string{"tcp"}, Ports: "22 8080", Action: core.ActionAccept,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 || cs[0].Subject != "VB-temp-wan-mgmt" {
		t.Errorf("staged rows = %+v, want one naming the rule", cs)
	}

	for name, committedName := range map[string]string{"named": "VB-temp-wan-mgmt", "unnamed": ""} {
		t.Run(name, func(t *testing.T) {
			committed := map[string]string{
				"firewall.cfg0992bd": "rule", "firewall.cfg0992bd.src": "wan", "firewall.cfg0992bd.proto": "tcp",
				"firewall.cfg0992bd.dest_port": "22 8080", "firewall.cfg0992bd.target": "ACCEPT",
			}
			if committedName != "" {
				committed["firewall.cfg0992bd.name"] = committedName
			}
			after := map[string]string{"firewall.cfg0992bd.enabled": "0"}
			for k, v := range committed {
				after[k] = v
			}
			r := &recordingRunner{
				values: after, committed: committed, sectionType: "rule",
				anonymous:   map[string]string{"cfg0992bd": "@rule[9]"},
				stagedLines: []string{"firewall.cfg0992bd.enabled='0'"},
			}
			read, err := (networkManager{run: r.run, configDir: t.TempDir()}).StagedChanges()
			if err != nil {
				t.Fatal(err)
			}
			want := entrySubject(committedName, "wan · tcp 22, 8080")
			if len(read) != 1 || read[0].Subject != want || read[0].LabelKey != "firewall.rule.enabled" {
				t.Errorf("read back = %+v, want one row about %q", read, want)
			}
		})
	}
}

func TestRulesNeedARunner(t *testing.T) {
	if _, err := (networkManager{}).StageRule(core.FirewallRuleConfig{}); !errors.Is(err, core.ErrNotImplemented) {
		t.Errorf("err = %v", err)
	}
	if _, err := (networkManager{}).RemoveRule("x"); !errors.Is(err, core.ErrNotImplemented) {
		t.Errorf("err = %v", err)
	}
}
