package openwrt

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/veilbridge-os/veilbridge/internal/adapters/openwrt/ubus"
	"github.com/veilbridge-os/veilbridge/internal/core"
)

// #53. A small uci that keeps its draft: `set` and `delete` change what
// `get` and `show` answer afterwards, the way the real one does. The fakes
// that only record calls cannot tell "staged the block" from "staged it and
// then read the list as if it were not there".
type draftUCI struct {
	base   string            // `uci show firewall` of the router, as captured
	values map[string]string // the panel's sections, draft applied
	calls  []string
	check  []string // what successive `fw4 check` calls print
}

const phoneMAC = "02:00:5e:00:53:d6" // RFC 7042 documentation address, private bit set

func (u *draftUCI) run(_ context.Context, name string, args ...string) ([]byte, error) {
	line := strings.TrimSpace(name + " " + strings.Join(args, " "))
	u.calls = append(u.calls, line)
	if !allowedCommands[name] {
		return nil, errors.New("refusing to run " + name + ": not in the adapter's allow-list")
	}
	if name == fw4Program {
		if len(u.check) == 0 {
			return []byte("Ruleset passes nftables check.\n"), nil
		}
		out := u.check[0]
		u.check = u.check[1:]
		return []byte(out), nil
	}
	switch {
	case len(args) == 3 && args[0] == "-q" && args[1] == "show" && args[2] == "firewall":
		return []byte(u.base + u.show()), nil
	case len(args) == 3 && args[0] == "-q" && args[1] == "get":
		if v, ok := u.values[args[2]]; ok {
			return []byte(v + "\n"), nil
		}
		return nil, errors.New("uci: Entry not found")
	case len(args) == 2 && args[0] == "set":
		k, v, _ := strings.Cut(args[1], "=")
		u.values[k] = v
		return nil, nil
	case len(args) == 2 && (args[0] == "delete" || args[0] == "revert"):
		for k := range u.values {
			if k == args[1] || strings.HasPrefix(k, args[1]+".") {
				delete(u.values, k)
			}
		}
		return nil, nil
	}
	return nil, nil
}

// show prints the panel's sections the way uci does: the type line, then the
// options, each value quoted.
func (u *draftUCI) show() string {
	keys := make([]string, 0, len(u.values))
	for k := range u.values {
		keys = append(keys, k)
	}
	sort.Strings(keys) // "firewall.x" sorts before "firewall.x.opt"
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "='" + u.values[k] + "'\n")
	}
	return b.String()
}

func (u *draftUCI) called(prefix string) bool {
	for _, c := range u.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func internetWriter(t *testing.T, values map[string]string, check ...string) (*deviceManager, *draftUCI) {
	t.Helper()
	if values == nil {
		values = map[string]string{}
	}
	u := &draftUCI{base: fixture(t, "firewall-25.12.5.txt"), values: values, check: check}
	n := networkManager{run: u.run, configDir: t.TempDir(), lookupInterfaces: func() ([]core.NetworkInterface, error) {
		return []core.NetworkInterface{{Name: "lan", Up: true}, {Name: "wan", Up: true}}, nil
	}}
	return newDeviceManager(n, ubus.NewWithRunner(u.run)), u
}

func blockedPhone() map[string]string {
	s := "firewall." + noInternetSection(phoneMAC)
	return map[string]string{
		s: "rule", s + ".name": "VeilBridge: no internet for " + phoneMAC, s + ".src": "lan",
		s + ".dest": "*", s + ".src_mac": phoneMAC, s + ".proto": "all", s + ".target": "REJECT",
	}
}

// The block has the shape measured with a phone in #50: from the local zone,
// from this address, to any zone, rejected — IPv4 and IPv6 alike, because no
// family is named. One dangerous row names the device; nothing is committed.
func TestTurningInternetOffStagesTheMeasuredRule(t *testing.T) {
	m, u := internetWriter(t, nil)
	cs, err := m.StageDeviceInternet("02-00-5E-00-53-D6", false)
	if err != nil {
		t.Fatal(err)
	}
	s := "firewall.vb_noinet_02005e0053d6"
	want := map[string]string{
		s: "rule", s + ".src": "lan", s + ".dest": "*", s + ".src_mac": phoneMAC,
		s + ".proto": "all", s + ".target": "REJECT",
	}
	for k, v := range want {
		if u.values[k] != v {
			t.Errorf("%s = %q, want %q", k, u.values[k], v)
		}
	}
	if _, ok := u.values[s+".family"]; ok {
		t.Error("the block names a family: one of IPv4 and IPv6 would stay open")
	}
	if len(cs) != 1 || cs[0].LabelKey != "firewall.noInternet.section" || cs[0].To != phoneMAC ||
		cs[0].From != "" || !cs[0].Dangerous || cs[0].Detail != s {
		t.Errorf("rows = %+v, want one dangerous row naming the device", cs)
	}
	if u.called("uci commit") {
		t.Fatal("the block was committed instead of staged")
	}
	checks := 0
	for _, c := range u.calls {
		if c == fw4Program+" check" {
			checks++
		}
	}
	if checks != 2 {
		t.Errorf("the firewall checked the draft %d times, want before and after (D-66)", checks)
	}
	// And the list reads it back: the draft is what the screen shows.
	if !blockedDevices(u.base + u.show())[phoneMAC] {
		t.Error("the staged block is not read back as a block")
	}
}

func TestTurningInternetOffTwiceIsOneRule(t *testing.T) {
	m, u := internetWriter(t, blockedPhone())
	cs, err := m.StageDeviceInternet(phoneMAC, false)
	if err != nil || len(cs) != 0 {
		t.Fatalf("rows = %+v, err = %v; want nothing to do", cs, err)
	}
	if u.called("uci set") {
		t.Error("an already blocked device was staged again")
	}
}

func TestTurningInternetBackOnRemovesTheRule(t *testing.T) {
	m, u := internetWriter(t, blockedPhone())
	cs, err := m.StageDeviceInternet(phoneMAC, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 || cs[0].LabelKey != "firewall.noInternet.section" || cs[0].From != phoneMAC || cs[0].To != "" {
		t.Errorf("rows = %+v, want one row taking the device's block away", cs)
	}
	if len(u.values) != 0 {
		t.Errorf("left behind: %v", u.values)
	}
	if blockedDevices(u.base + u.show())[phoneMAC] {
		t.Error("still read as blocked")
	}
}

func TestTurningOnInternetThatIsOnChangesNothing(t *testing.T) {
	m, u := internetWriter(t, nil)
	cs, err := m.StageDeviceInternet(phoneMAC, true)
	if err != nil || len(cs) != 0 {
		t.Fatalf("rows = %+v, err = %v; want nothing to do", cs, err)
	}
	if u.called("uci delete") || u.called("uci set") {
		t.Error("staged something for a device whose internet is on")
	}
}

// Somebody paused the block in LuCI. Turning it "off" again from the panel
// must not silently switch their pause back on.
func TestABlockPausedByHandIsNotSwitchedBackOnQuietly(t *testing.T) {
	v := blockedPhone()
	v["firewall."+noInternetSection(phoneMAC)+".enabled"] = "0"
	m, u := internetWriter(t, v)
	_, err := m.StageDeviceInternet(phoneMAC, false)
	var r *core.FieldError
	if !errors.As(err, &r) || r.Field != "mac" {
		t.Fatalf("err = %v, want a refusal", err)
	}
	if u.called("uci set") {
		t.Error("the paused block was changed")
	}
	if blockedDevices(u.base + u.show())[phoneMAC] {
		t.Error("a paused block reads as a block")
	}
}

// Refused by the router's firewall: only this device's section is taken
// back. Reverting the whole firewall draft would throw away somebody's port
// forward that is staged and waiting in the apply bar.
func TestARefusedBlockTakesBackOnlyItself(t *testing.T) {
	skipped := "[!] Section vb_noinet_02005e0053d6 skipped due to invalid options\n"
	m, u := internetWriter(t, map[string]string{"firewall.other": "redirect"}, "Ruleset passes nftables check.\n", skipped)
	_, err := m.StageDeviceInternet(phoneMAC, false)
	if err == nil || !strings.Contains(err.Error(), "would skip") {
		t.Fatalf("err = %v, want the firewall's refusal", err)
	}
	if u.called("uci revert firewall\x00") || u.calls[len(u.calls)-1] != "uci revert firewall.vb_noinet_02005e0053d6" {
		t.Errorf("last call %q, want the block's own section reverted", u.calls[len(u.calls)-1])
	}
	if u.values["firewall.other"] != "redirect" {
		t.Error("somebody else's draft was thrown away")
	}
	if _, ok := u.values["firewall.vb_noinet_02005e0053d6"]; ok {
		t.Error("the refused block was left in the draft")
	}
}

func TestTurningInternetOffRefusesWhatIsNotADevice(t *testing.T) {
	m, u := internetWriter(t, nil)
	for _, bad := range []string{"", "not-a-mac", "01:00:5e:00:00:01"} {
		_, err := m.StageDeviceInternet(bad, false)
		var r *core.FieldError
		if !errors.As(err, &r) || r.Field != "mac" {
			t.Errorf("%q: err = %v, want a refusal of the address", bad, err)
		}
	}
	if u.called("uci set") {
		t.Error("staged a block for something that is not a device")
	}
}

// Only the panel's own sections are blocks: a rule somebody wrote by hand
// with a source address is theirs, and the list must not claim it.
func TestOnlyThePanelsOwnRulesAreBlocks(t *testing.T) {
	show := "firewall.mine=rule\nfirewall.mine.src_mac='02:00:5e:00:53:01'\nfirewall.mine.target='REJECT'\n" +
		"firewall.vb_noinet_02005e005302=rule\nfirewall.vb_noinet_02005e005302.src_mac='02:00:5E:00:53:02'\n" +
		"firewall.vb_noinet_02005e005303=redirect\nfirewall.vb_noinet_02005e005303.src_mac='02:00:5e:00:53:03'\n"
	got := blockedDevices(show)
	if len(got) != 1 || !got["02:00:5e:00:53:02"] {
		t.Errorf("blocked = %v, want only the panel's rule, address normalised", got)
	}
}

// A blocked device the router does not see now stays on the list: its block
// is still there, and a block you cannot see is one you cannot lift.
func TestABlockedDeviceNobodyHearsStaysOnTheList(t *testing.T) {
	fx := newDevFixture(t, cudy(), "")
	fx.runner.out["uci -q show firewall"] = []byte(
		"firewall.vb_noinet_02005e005377=rule\nfirewall.vb_noinet_02005e005377.src_mac='02:00:5e:00:53:77'\n")
	got := fx.list()
	d, ok := got["02:00:5e:00:53:77"]
	if !ok {
		t.Fatalf("the blocked device is not on the list: %v", got)
	}
	if d.Internet != core.InternetBlocked || d.Online || d.Link.Kind != core.LinkUnknown {
		t.Errorf("device = %+v, want blocked, not online, connection unknown", d)
	}
	if p := got[phoneMAC]; p.Internet != core.InternetAllowed {
		t.Errorf("the phone on Wi-Fi reads internet %q, want allowed", p.Internet)
	}
}

// The apply bar reads the draft back from `uci changes`: the block appears as
// one row naming the device, and so does its removal — never "Block traffic
// through the router · lan → *" and six field rows.
func TestTheBlockReadsBackAsOneRowNamingTheDevice(t *testing.T) {
	s := "firewall.vb_noinet_02005e0053d6"
	for _, tc := range []struct {
		name      string
		values    map[string]string // draft applied
		committed map[string]string
		lines     []string
		from, to  string
	}{
		{
			name: "turned off", values: blockedPhone(), committed: map[string]string{},
			lines: []string{s + "='rule'", s + ".name='VeilBridge: no internet for " + phoneMAC + "'",
				s + ".src='lan'", s + ".dest='*'", s + ".src_mac='" + phoneMAC + "'",
				s + ".proto='all'", s + ".target='REJECT'"},
			to: phoneMAC,
		},
		{
			name: "back on", values: map[string]string{}, committed: blockedPhone(),
			lines: []string{"-" + s}, from: phoneMAC,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &recordingRunner{values: tc.values, committed: tc.committed, sectionType: "rule", stagedLines: tc.lines}
			cs, err := (networkManager{run: r.run, configDir: t.TempDir()}).StagedChanges()
			if err != nil {
				t.Fatal(err)
			}
			if len(cs) != 1 || cs[0].LabelKey != "firewall.noInternet.section" ||
				cs[0].From != tc.from || cs[0].To != tc.to || !cs[0].Dangerous {
				t.Fatalf("rows = %+v, want one row %q → %q", cs, tc.from, tc.to)
			}
			assertKnownKeys(t, tc.name, cs)
		})
	}
}
