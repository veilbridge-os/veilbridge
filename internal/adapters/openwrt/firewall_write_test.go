package openwrt

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/veilbridge-os/veilbridge/internal/core"
)

// fwRunner answers the two things the firewall writer asks that the uci fake
// does not: the firewall as `uci show` prints it, and `fw4 check`. The check
// answers are a sequence, because the writer asks twice — before staging, for
// the warnings that are somebody else's, and after, for the ones it caused.
type fwRunner struct {
	uci   *recordingRunner
	show  string
	check []string
}

func (f *fwRunner) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name == fw4Program {
		f.uci.calls = append(f.uci.calls, append([]string{name}, args...))
		if len(f.check) == 0 {
			return []byte("Ruleset passes nftables check.\n"), nil
		}
		out := f.check[0]
		f.check = f.check[1:]
		return []byte(out), nil
	}
	if name == "uci" && strings.Join(args, " ") == "-q show firewall" {
		return []byte(f.show), nil
	}
	return f.uci.run(ctx, name, args...)
}

func firewallWriter(t *testing.T, show string, check ...string) (networkManager, *fwRunner) {
	t.Helper()
	f := &fwRunner{uci: &recordingRunner{values: map[string]string{}}, show: show, check: check}
	return networkManager{
		run:       f.run,
		configDir: t.TempDir(),
		lookupInterfaces: func() ([]core.NetworkInterface, error) {
			return []core.NetworkInterface{
				{Name: "lan", Up: true, IPv4: []string{"192.168.1.1/24"}},
				{Name: "wan", Up: true, IPv4: []string{"198.51.100.242/24"}},
			}, nil
		},
	}, f
}

func (f *fwRunner) called(prefix string) bool {
	for _, c := range f.uci.calls {
		if strings.HasPrefix(strings.Join(c, " "), prefix) {
			return true
		}
	}
	return false
}

func TestANewPortForwardIsStagedAsOneRowAndNeverCommitted(t *testing.T) {
	m, f := firewallWriter(t, fixture(t, "firewall-25.12.5.txt"))
	cs, err := m.StagePortForward(core.PortForwardConfig{
		Name: "NAS", Enabled: true, Protocols: []string{"tcp"},
		ExternalPort: "8443", ToAddress: "192.168.1.50", ToPort: "443",
	})
	if err != nil {
		t.Fatal(err)
	}
	staged := strings.Join(f.uci.sets(), "|")
	for _, want := range []string{
		"firewall.cfg05fe63.target=DNAT", "firewall.cfg05fe63.src=wan", "firewall.cfg05fe63.dest=lan",
		"firewall.cfg05fe63.proto=tcp", "firewall.cfg05fe63.src_dport=8443",
		"firewall.cfg05fe63.dest_ip=192.168.1.50", "firewall.cfg05fe63.dest_port=443",
		"firewall.cfg05fe63.name=NAS",
	} {
		if !strings.Contains(staged, want) {
			t.Errorf("staged %q, want it to contain %q", staged, want)
		}
	}
	if len(cs) != 1 || cs[0].LabelKey != "firewall.redirect.section" ||
		cs[0].To != "tcp 8443 → 192.168.1.50:443" || !cs[0].Dangerous {
		t.Errorf("rows = %+v, want one dangerous row in words", cs)
	}
	if f.called("uci commit") {
		t.Fatal("a port forward was committed instead of staged")
	}
	if !f.called(fw4Program + " check") {
		t.Error("the firewall was never asked whether it accepts the draft (D-66)")
	}
}

func TestPortForwardRefusalsNameTheirField(t *testing.T) {
	ok := core.PortForwardConfig{Enabled: true, Protocols: []string{"tcp"}, ExternalPort: "8443", ToAddress: "192.168.1.50"}
	cases := map[string]func(c *core.PortForwardConfig){
		"protocols/none":        func(c *core.PortForwardConfig) { c.Protocols = nil },
		"protocols/icmp":        func(c *core.PortForwardConfig) { c.Protocols = []string{"icmp"} },
		"externalPort/too-big":  func(c *core.PortForwardConfig) { c.ExternalPort = "99999" },
		"externalPort/zero":     func(c *core.PortForwardConfig) { c.ExternalPort = "0" },
		"externalPort/reversed": func(c *core.PortForwardConfig) { c.ExternalPort = "10-5" },
		"externalPort/word":     func(c *core.PortForwardConfig) { c.ExternalPort = "http" },
		"toPort/other-length":   func(c *core.PortForwardConfig) { c.ExternalPort = "8000-8010"; c.ToPort = "80-82" },
		"toAddress/not-address": func(c *core.PortForwardConfig) { c.ToAddress = "nas" },
		"toAddress/outside-lan": func(c *core.PortForwardConfig) { c.ToAddress = "10.0.0.5" },
		"name/quote":            func(c *core.PortForwardConfig) { c.Name = "it's" },
		"id/unknown":            func(c *core.PortForwardConfig) { c.ID = "@redirect[7]" },
		"id/not-an-entry":       func(c *core.PortForwardConfig) { c.ID = "wan;reboot" },
	}
	for name, edit := range cases {
		field, _, _ := strings.Cut(name, "/")
		t.Run(name, func(t *testing.T) {
			m, f := firewallWriter(t, fixture(t, "firewall-25.12.5.txt"))
			cfg := ok
			edit(&cfg)
			_, err := m.StagePortForward(cfg)
			assertRefusedField(t, err, field)
			if len(f.uci.sets()) != 0 || f.called("uci add") {
				t.Errorf("the device was touched before refusing: %v", f.uci.calls)
			}
		})
	}
}

// D-66: fw4 exits 0 on a section it will skip. A warning that appears only
// after staging is this draft's doing, and the draft goes.
func TestADraftTheFirewallWouldSkipIsDropped(t *testing.T) {
	skipped := "Ruleset passes nftables check.\n[!] Section @redirect[0] (NAS) option 'dest_ip' specifies invalid value\n"
	m, f := firewallWriter(t, fixture(t, "firewall-25.12.5.txt"), "Ruleset passes nftables check.\n", skipped)
	_, err := m.StagePortForward(core.PortForwardConfig{
		Enabled: true, Protocols: []string{"tcp"}, ExternalPort: "8443", ToAddress: "192.168.1.50",
	})
	if err == nil || !strings.Contains(err.Error(), "would skip") {
		t.Fatalf("err = %v, want a refusal quoting the firewall", err)
	}
	if !f.called("uci revert firewall") {
		t.Error("the refused draft was left on the device")
	}
}

// A warning that was there before belongs to somebody else's section and
// must not block an unrelated change.
func TestAnOldWarningDoesNotBlockANewForward(t *testing.T) {
	old := "Ruleset passes nftables check.\n[!] Section @rule[12] (LuCI thing) skipped due to invalid options\n"
	m, f := firewallWriter(t, fixture(t, "firewall-25.12.5.txt"), old, old)
	if _, err := m.StagePortForward(core.PortForwardConfig{
		Enabled: true, Protocols: []string{"tcp", "udp"}, ExternalPort: "51820", ToAddress: "192.168.1.60",
	}); err != nil {
		t.Fatalf("blocked by a warning that was already there: %v", err)
	}
	if f.called("uci revert firewall") {
		t.Error("the draft was dropped for somebody else's warning")
	}
}

const showWithForward = `firewall.@zone[0]=zone
firewall.@zone[0].name='lan'
firewall.@zone[0].network='lan'
firewall.@zone[1]=zone
firewall.@zone[1].name='wan'
firewall.@zone[1].network='wan'
firewall.@zone[1].masq='1'
firewall.@redirect[0]=redirect
firewall.@redirect[0].name='NAS'
firewall.@redirect[0].src='wan'
firewall.@redirect[0].proto='tcp'
firewall.@redirect[0].src_dport='8443'
firewall.@redirect[0].dest_ip='192.168.1.50'
firewall.@redirect[0].dest_port='443'
firewall.@redirect[0].target='DNAT'
`

func TestRemovingAPortForwardIsOneRowNamedInWords(t *testing.T) {
	m, f := firewallWriter(t, showWithForward)
	cs, err := m.RemovePortForward("@redirect[0]")
	if err != nil {
		t.Fatal(err)
	}
	if !f.called("uci delete firewall.@redirect[0]") {
		t.Errorf("calls = %v, want the entry deleted by its own id", f.uci.calls)
	}
	if len(cs) != 1 || cs[0].From != "tcp 8443 → 192.168.1.50:443" || cs[0].To != "" || cs[0].LabelKey != "firewall.redirect.section" {
		t.Errorf("rows = %+v", cs)
	}
	if _, err := m.RemovePortForward("@redirect[3]"); err == nil {
		t.Error("removing a forward that is not there was accepted")
	}
	if _, err := m.RemovePortForward("@redirect[0];reboot"); err == nil {
		t.Error("an id that is not an entry was accepted")
	}
}

func TestEditingAForwardStagesOnlyWhatChanged(t *testing.T) {
	m, f := firewallWriter(t, showWithForward)
	f.uci.values = map[string]string{
		"firewall.@redirect[0].target": "DNAT", "firewall.@redirect[0].src": "wan", "firewall.@redirect[0].dest": "lan",
		"firewall.@redirect[0].proto": "tcp", "firewall.@redirect[0].src_dport": "8443",
		"firewall.@redirect[0].dest_ip": "192.168.1.50", "firewall.@redirect[0].dest_port": "443",
		"firewall.@redirect[0].name": "NAS",
	}
	cs, err := m.StagePortForward(core.PortForwardConfig{
		ID: "@redirect[0]", Name: "NAS", Enabled: false, Protocols: []string{"tcp"},
		ExternalPort: "8443", ToAddress: "192.168.1.50", ToPort: "443",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 || cs[0].LabelKey != "firewall.redirect.enabled" || cs[0].To != "0" {
		t.Errorf("rows = %+v, want one row: the forward switched off", cs)
	}
	if f.called("uci add") {
		t.Error("editing an entry added a new one")
	}
}

func TestPortForwardsNeedARunner(t *testing.T) {
	if _, err := (networkManager{}).StagePortForward(core.PortForwardConfig{}); !errors.Is(err, core.ErrNotImplemented) {
		t.Errorf("err = %v", err)
	}
	if _, err := (networkManager{}).RemovePortForward("x"); !errors.Is(err, core.ErrNotImplemented) {
		t.Errorf("err = %v", err)
	}
}
