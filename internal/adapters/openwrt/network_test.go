package openwrt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/veilbridge-os/veilbridge/internal/adapters/openwrt/ubus"
	"github.com/veilbridge-os/veilbridge/internal/core"
)

// The fixtures are the same verbatim captures the ubus package replays, read
// from its testdata rather than copied: a second copy would drift, and the
// whole point of having two branches is that they still disagree tomorrow.
//
//	23.05 (x86 VM)  lan, lanwan, loopback — TWO interfaces with a default route
//	25.12 (Cudy)    lan, loopback, wan, wan6 — one IPv4 uplink, plus IPv6
//
// The x86 stand is not a contrived case: it is a real device on which "which
// one is the WAN" has no measured answer, and it is why WANStatus reports the
// rule it used.
var ubusBranches = []string{"23.05", "25.12"}

func busFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("ubus", "testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// busReplay answers ubus calls from fixtures, dispatching on the object and
// method actually asked for. Anything unexpected is a test failure and not a
// silent empty reply: a manager that quietly asks for the wrong object would
// otherwise look like it worked.
func busReplay(t *testing.T, branch string, calls *int) *ubus.Client {
	t.Helper()
	return ubus.NewWithRunner(func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if calls != nil {
			*calls++
		}
		switch strings.Join(args, " ") {
		case "call network.interface dump":
			return busFixture(t, "ifdump-"+branch+".json"), nil
		case "call system board":
			return busFixture(t, "board-"+branch+".json"), nil
		case "call system info":
			return busFixture(t, "info-"+branch+".json"), nil
		}
		t.Fatalf("unexpected ubus call: %v", args)
		return nil, nil
	})
}

// busJSON answers every call with one fabricated reply, for the shapes no real
// stand produces (a router with no uplink at all).
func busJSON(body string) *ubus.Client {
	return ubus.NewWithRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte(body), nil
	})
}

func TestInterfacesParsedFromBothBranches(t *testing.T) {
	// Field by field on the one interface each branch agrees to have, plus the
	// uplink: a wrong mapping would otherwise reach the dashboard as a blank.
	want := map[string]core.NetworkInterface{
		"23.05": {
			Name: "lanwan", Device: "eth1", Up: true, Proto: "dhcp",
			IPv4: []string{"198.51.100.116/24"}, Gateway: "198.51.100.1",
			DNS: []string{"198.51.100.1"},
		},
		"25.12": {
			Name: "wan", Device: "wan", Up: true, Proto: "dhcp",
			IPv4: []string{"198.51.100.242/24"}, Gateway: "198.51.100.1",
			DNS: []string{"198.51.100.1"},
		},
	}
	for _, branch := range ubusBranches {
		t.Run(branch, func(t *testing.T) {
			m := newNetworkManager(busReplay(t, branch, nil))
			got, err := m.Interfaces()
			if err != nil {
				t.Fatalf("interfaces: %v", err)
			}
			if len(got) == 0 {
				t.Fatal("no interfaces parsed")
			}
			// Sorted by name, always: netifd's own order changes under
			// reconfiguration and a list that reshuffles is a UI defect.
			for i := 1; i < len(got); i++ {
				if got[i-1].Name > got[i].Name {
					t.Errorf("interfaces not sorted: %s before %s", got[i-1].Name, got[i].Name)
				}
			}
			w := want[branch]
			var found *core.NetworkInterface
			for i := range got {
				if got[i].Name == w.Name {
					found = &got[i]
				}
			}
			if found == nil {
				t.Fatalf("interface %q missing from %+v", w.Name, got)
			}
			if found.Device != w.Device || found.Up != w.Up || found.Proto != w.Proto ||
				found.Gateway != w.Gateway ||
				fmt.Sprint(found.IPv4) != fmt.Sprint(w.IPv4) ||
				fmt.Sprint(found.DNS) != fmt.Sprint(w.DNS) {
				t.Errorf("interface mismatch:\n got %+v\nwant %+v", *found, w)
			}
			if found.UptimeSec <= 0 {
				t.Errorf("uptime not parsed: %d", found.UptimeSec)
			}
		})
	}
}

// The address is reported as CIDR because a mask is meaningless once separated
// from its address, and every consumer would otherwise re-join them itself.
func TestAddressesAreRenderedAsCIDR(t *testing.T) {
	m := newNetworkManager(busReplay(t, "25.12", nil))
	got, _ := m.Interfaces()
	for _, i := range got {
		if i.Name != "lan" {
			continue
		}
		if len(i.IPv4) != 1 || i.IPv4[0] != "192.168.1.1/24" {
			t.Errorf("lan address = %v, want [192.168.1.1/24]", i.IPv4)
		}
		return
	}
	t.Fatal("lan interface missing")
}

// IPv6 is not folded into the IPv4 answer: a caller dialing an IPv4 address is
// not helped by an fe80:: nexthop, so an IPv6 default route must land in
// Gateway6 and nowhere else.
//
// The 25.12 capture cannot prove this on its own — that stand's wan6 has
// prefix routes but no ::/0 — so the assertion is made against a dump that
// does have one. A test that can only pass is not a test.
func TestIPv6GatewayIsSeparateFromIPv4(t *testing.T) {
	m := newNetworkManager(busJSON(`{"interface":[
		{"interface":"wan6","up":true,"proto":"dhcpv6","l3_device":"wan",
		 "ipv6-address":[{"address":"2001:db8::1","mask":64}],
		 "route":[{"target":"::","mask":0,"nexthop":"fe80::1"}]}]}`))

	got, err := m.Interfaces()
	if err != nil || len(got) != 1 {
		t.Fatalf("interfaces = %d, err = %v", len(got), err)
	}
	if got[0].Gateway6 != "fe80::1" {
		t.Errorf("gateway6 = %q, want fe80::1", got[0].Gateway6)
	}
	if got[0].Gateway != "" {
		t.Errorf("an IPv6 nexthop leaked into the IPv4 gateway: %q", got[0].Gateway)
	}
	if len(got[0].IPv6) != 1 || got[0].IPv6[0] != "2001:db8::1/64" {
		t.Errorf("ipv6 = %v, want [2001:db8::1/64]", got[0].IPv6)
	}
	// An IPv6-only uplink is still an uplink.
	wan, err := m.WANInfo()
	if err != nil {
		t.Fatalf("wan: %v", err)
	}
	if wan.Interface.Name != "wan6" {
		t.Errorf("wan = %q, want wan6", wan.Interface.Name)
	}

	// And the real 25.12 capture must not invent an IPv4 gateway on wan6.
	real := newNetworkManager(busReplay(t, "25.12", nil))
	ifaces, _ := real.Interfaces()
	for _, i := range ifaces {
		if i.Name == "wan6" && i.Gateway != "" {
			t.Errorf("wan6 reports an IPv4 gateway %q on the real capture", i.Gateway)
		}
	}
}

func TestWANSelection(t *testing.T) {
	cases := map[string]struct {
		branch     string
		wantName   string
		wantRule   string
		wantCandis int
	}{
		// One default route: the answer is measured, not guessed.
		"25.12 single candidate": {branch: "25.12", wantName: "wan", wantRule: "default-route", wantCandis: 1},
		// Two default routes on the x86 stand. "lanwan" is the uplink the
		// stand is actually reached on, and the naming convention is the only
		// thing that says so — which is exactly what SelectedBy must admit.
		"23.05 two candidates": {branch: "23.05", wantName: "lanwan", wantRule: "name", wantCandis: 2},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := newNetworkManager(busReplay(t, tc.branch, nil))
			wan, err := m.WANInfo()
			if err != nil {
				t.Fatalf("wan: %v", err)
			}
			if wan.Interface.Name != tc.wantName {
				t.Errorf("wan = %q, want %q", wan.Interface.Name, tc.wantName)
			}
			if wan.SelectedBy != tc.wantRule {
				t.Errorf("selectedBy = %q, want %q", wan.SelectedBy, tc.wantRule)
			}
			if len(wan.Candidates) != tc.wantCandis {
				t.Errorf("candidates = %v, want %d of them", wan.Candidates, tc.wantCandis)
			}
			if !wan.Interface.HasDefaultRoute() {
				t.Error("selected WAN has no default route")
			}
		})
	}
}

// A router with no uplink is a state, not a failure: an unconfigured device
// answers exactly this, and the caller must be able to tell it apart from a
// dead ubus.
func TestWANWithoutDefaultRouteIsNamedState(t *testing.T) {
	m := newNetworkManager(busJSON(`{"interface":[
		{"interface":"lan","up":true,"proto":"static","device":"br-lan","l3_device":"br-lan",
		 "ipv4-address":[{"address":"192.168.1.1","mask":24}],"route":[]},
		{"interface":"loopback","up":true,"proto":"static","l3_device":"lo",
		 "ipv4-address":[{"address":"127.0.0.1","mask":8}],"route":[]}]}`))

	if _, err := m.WANInfo(); !errors.Is(err, core.ErrNoWAN) {
		t.Fatalf("WANInfo err = %v, want ErrNoWAN", err)
	}
	// The interfaces themselves are still listed: a device with no uplink is
	// precisely when an operator needs to see what it does have.
	got, err := m.Interfaces()
	if err != nil || len(got) != 2 {
		t.Fatalf("interfaces = %d, err = %v", len(got), err)
	}
}

// Loopback has no default route, but it must never become a WAN candidate even
// if a device somehow installs one on it.
func TestLoopbackIsNeverTheWAN(t *testing.T) {
	m := newNetworkManager(busJSON(`{"interface":[
		{"interface":"loopback","up":true,"proto":"static","l3_device":"lo",
		 "route":[{"target":"0.0.0.0","mask":0,"nexthop":"127.0.0.1"}]},
		{"interface":"uplink","up":true,"proto":"dhcp","l3_device":"eth0",
		 "route":[{"target":"0.0.0.0","mask":0,"nexthop":"198.51.100.1"}]}]}`))

	wan, err := m.WANInfo()
	if err != nil {
		t.Fatalf("wan: %v", err)
	}
	if wan.Interface.Name != "uplink" {
		t.Errorf("wan = %q, want uplink", wan.Interface.Name)
	}
	if wan.SelectedBy != "default-route" {
		t.Errorf("selectedBy = %q, want default-route (loopback is not a candidate)", wan.SelectedBy)
	}
}

// Two uplinks, neither named "wan": there is no honest rule left, so the
// answer says "first-candidate" instead of pretending to know. It must also be
// stable across polls — a dashboard that alternates between two WANs is worse
// than one that picks the wrong one consistently.
func TestAmbiguousWANAdmitsTheCoinToss(t *testing.T) {
	m := newNetworkManager(busJSON(`{"interface":[
		{"interface":"zulu","up":true,"proto":"dhcp","l3_device":"eth1",
		 "route":[{"target":"0.0.0.0","mask":0,"nexthop":"198.51.100.1"}]},
		{"interface":"alpha","up":true,"proto":"dhcp","l3_device":"eth0",
		 "route":[{"target":"0.0.0.0","mask":0,"nexthop":"198.51.100.2"}]}]}`))

	first, err := m.WANInfo()
	if err != nil {
		t.Fatalf("wan: %v", err)
	}
	if first.SelectedBy != "first-candidate" {
		t.Errorf("selectedBy = %q, want first-candidate", first.SelectedBy)
	}
	if first.Interface.Name != "alpha" {
		t.Errorf("wan = %q, want alpha (sorted first)", first.Interface.Name)
	}
	if len(first.Candidates) != 2 {
		t.Errorf("both candidates must be reported, got %v", first.Candidates)
	}
	again, _ := m.WANInfo()
	if again.Interface.Name != first.Interface.Name {
		t.Errorf("unstable pick: %q then %q", first.Interface.Name, again.Interface.Name)
	}
}

// Two names containing "wan" is not a tie-break, it is a coin toss, and the
// name rule must decline rather than pick the last one it saw.
func TestTwoWanishNamesDoNotTriggerTheNameRule(t *testing.T) {
	m := newNetworkManager(busJSON(`{"interface":[
		{"interface":"wan_backup","up":true,"proto":"dhcp","l3_device":"eth1",
		 "route":[{"target":"0.0.0.0","mask":0,"nexthop":"198.51.100.1"}]},
		{"interface":"wan_main","up":true,"proto":"dhcp","l3_device":"eth0",
		 "route":[{"target":"0.0.0.0","mask":0,"nexthop":"198.51.100.2"}]}]}`))

	wan, err := m.WANInfo()
	if err != nil {
		t.Fatalf("wan: %v", err)
	}
	if wan.SelectedBy != "first-candidate" {
		t.Errorf("selectedBy = %q, want first-candidate", wan.SelectedBy)
	}
}

// An interface literally called "wan" beats one that merely contains it.
func TestExactWanNameWins(t *testing.T) {
	m := newNetworkManager(busJSON(`{"interface":[
		{"interface":"lanwan","up":true,"proto":"dhcp","l3_device":"eth1",
		 "route":[{"target":"0.0.0.0","mask":0,"nexthop":"198.51.100.1"}]},
		{"interface":"wan","up":true,"proto":"dhcp","l3_device":"eth0",
		 "route":[{"target":"0.0.0.0","mask":0,"nexthop":"198.51.100.2"}]}]}`))

	wan, err := m.WANInfo()
	if err != nil {
		t.Fatalf("wan: %v", err)
	}
	if wan.Interface.Name != "wan" || wan.SelectedBy != "name" {
		t.Errorf("got %q by %q, want wan by name", wan.Interface.Name, wan.SelectedBy)
	}
}

// A dead netifd must surface as an error, never as "this router has no
// interfaces": the second reads like a fact about the device.
func TestUbusFailureIsAnErrorNotAnEmptyRouter(t *testing.T) {
	boom := ubus.NewWithRunner(func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("Command failed: Not found")
	})
	m := newNetworkManager(boom)

	got, err := m.Interfaces()
	if err == nil {
		t.Fatalf("expected an error, got %d interfaces", len(got))
	}
	if !strings.Contains(err.Error(), "Not found") {
		t.Errorf("the device's own words must survive: %v", err)
	}
	if _, err := m.WANInfo(); err == nil || errors.Is(err, core.ErrNoWAN) {
		t.Errorf("a broken bus must not read as ErrNoWAN, got %v", err)
	}
}

// Without ubus (a dev machine, a future platform) the manager says so, rather
// than reporting a router with no network.
func TestNoBusReportsNotImplemented(t *testing.T) {
	m := newNetworkManager(nil)
	if _, err := m.Interfaces(); !errors.Is(err, core.ErrNotImplemented) {
		t.Errorf("Interfaces err = %v, want ErrNotImplemented", err)
	}
	if _, err := m.WANInfo(); !errors.Is(err, core.ErrNotImplemented) {
		t.Errorf("WANInfo err = %v, want ErrNotImplemented", err)
	}
}
