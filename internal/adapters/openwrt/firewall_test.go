package openwrt

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/veilbridge-os/veilbridge/internal/core"
)

// Fixtures are `uci -q show firewall` captured from the two stands on
// 2026-09-25, unchanged except that the stands' network is replaced with the
// documentation range.
func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func zoneByName(t *testing.T, st core.FirewallStatus, name string) core.FirewallZone {
	t.Helper()
	for _, z := range st.Zones {
		if z.Name == name {
			return z
		}
	}
	t.Fatalf("no zone %q in %+v", name, st.Zones)
	return core.FirewallZone{}
}

func TestTheRoutersFirewallReadsAsTheDeviceHasIt(t *testing.T) {
	st := parseFirewall(fixture(t, "firewall-25.12.5.txt"), map[string]bool{"lan": true, "wan": true, "wan6": true})

	wan := zoneByName(t, st, "wan")
	if wan.Role != core.ZoneInternet || !wan.Masquerade || !wan.Live {
		t.Errorf("wan zone = %+v, want the live internet side with NAT", wan)
	}
	if wan.Input != core.ActionReject || wan.Forward != core.ActionReject || wan.Output != core.ActionAccept {
		t.Errorf("wan policies = %s/%s/%s, want reject/accept/reject", wan.Input, wan.Output, wan.Forward)
	}
	if !slices.Equal(wan.Networks, []string{"wan", "wan6"}) {
		t.Errorf("wan networks = %v", wan.Networks)
	}
	if lan := zoneByName(t, st, "lan"); lan.Role != core.ZoneLocal {
		t.Errorf("lan zone role = %q, want local", lan.Role)
	}
	if len(st.Forwardings) != 1 || st.Forwardings[0] != (core.ZoneForwarding{From: "lan", To: "wan"}) {
		t.Errorf("forwardings = %+v", st.Forwardings)
	}

	// Nine rules as the package ships them, plus the one that keeps the
	// panel reachable from the uplink side on this router. That one is ours
	// and must never be shown as untouchable system furniture: it is exactly
	// the rule an operator has to be able to find and change.
	var system, own []string
	for _, r := range st.Rules {
		if r.System {
			system = append(system, r.Name)
		} else {
			own = append(own, r.Name)
		}
	}
	if len(system) != 9 {
		t.Errorf("system rules = %d %v, want the 9 the package ships", len(system), system)
	}
	if !slices.Equal(own, []string{"VB-temp-wan-mgmt"}) {
		t.Errorf("rules a person made = %v, want just the management rule", own)
	}
	for _, r := range st.Rules {
		if r.Name == "VB-temp-wan-mgmt" {
			if !slices.Equal(r.Protocols, []string{"tcp"}) || r.Ports != "22 8080" || r.From != "wan" || r.Action != core.ActionAccept || !r.Enabled {
				t.Errorf("management rule = %+v", r)
			}
		}
	}
}

// D-68 on the stand where a name would lie: the uplink `lanwan` sits in the
// zone called "lan", and the zone called "wan" has nothing live in it.
func TestZoneRolesComeFromWhatZonesDoNotFromTheirNames(t *testing.T) {
	st := parseFirewall(fixture(t, "firewall-23.05.5.txt"), map[string]bool{"lan": true, "lanwan": true})
	lan := zoneByName(t, st, "lan")
	if lan.Role != core.ZoneLocal || !lan.Live || !slices.Equal(lan.Networks, []string{"lan", "lanwan"}) {
		t.Errorf("lan zone = %+v", lan)
	}
	wan := zoneByName(t, st, "wan")
	if wan.Role != core.ZoneInternet {
		t.Errorf("the NAT zone is %q, want internet", wan.Role)
	}
	if wan.Live {
		t.Error("the wan zone has no live connection on this stand, and must say so")
	}
}

// D-67: equal content is system, changed content is not — including a change
// as small as switching the rule off, which is a decision a person made.
func TestAChangedStockRuleIsNoLongerTheSystems(t *testing.T) {
	show := fixture(t, "firewall-25.12.5.txt")
	edited := strings.Replace(show,
		"firewall.@rule[1].target='ACCEPT'",
		"firewall.@rule[1].target='ACCEPT'\nfirewall.@rule[1].enabled='0'", 1)
	if edited == show {
		t.Fatal("fixture changed shape; the edit did not apply")
	}
	for _, r := range parseFirewall(edited, nil).Rules {
		if r.Name == "Allow-Ping" {
			if r.System || r.Enabled {
				t.Errorf("switched-off Allow-Ping = %+v, want a person's disabled rule", r)
			}
			return
		}
	}
	t.Fatal("Allow-Ping not found")
}

func TestAPortForwardIsReadWithTheFirewallsDefaults(t *testing.T) {
	show := `firewall.@redirect[0]=redirect
firewall.@redirect[0].name='NAS'
firewall.@redirect[0].src='wan'
firewall.@redirect[0].src_dport='8443'
firewall.@redirect[0].dest='lan'
firewall.@redirect[0].dest_ip='192.168.1.50'
firewall.@redirect[1]=redirect
firewall.@redirect[1].name='Camera'
firewall.@redirect[1].src='wan'
firewall.@redirect[1].proto='tcp'
firewall.@redirect[1].src_dport='10554'
firewall.@redirect[1].dest_ip='192.168.1.60'
firewall.@redirect[1].dest_port='554'
firewall.@redirect[1].target='DNAT'
firewall.@redirect[1].enabled='0'
firewall.@redirect[2]=redirect
firewall.@redirect[2].name='Outbound SNAT'
firewall.@redirect[2].target='SNAT'
`
	pf := parseFirewall(show, nil).PortForwards
	if len(pf) != 2 {
		t.Fatalf("port forwards = %+v, want two (a source-NAT redirect is not one)", pf)
	}
	nas := pf[0]
	if !slices.Equal(nas.Protocols, []string{"tcp", "udp"}) || nas.ToPort != "8443" || !nas.Enabled || nas.ID != "@redirect[0]" {
		t.Errorf("NAS = %+v, want tcp+udp by default and the same port inside", nas)
	}
	cam := pf[1]
	if cam.Enabled || cam.ToPort != "554" || cam.ExternalPort != "10554" || !slices.Equal(cam.Protocols, []string{"tcp"}) {
		t.Errorf("Camera = %+v", cam)
	}
}

// An empty firewall still answers with empty lists, so the screen renders
// "nothing here" instead of failing on null.
func TestAnEmptyFirewallIsEmptyListsNotNull(t *testing.T) {
	st := parseFirewall("", nil)
	if st.Zones == nil || st.Forwardings == nil || st.PortForwards == nil || st.Rules == nil {
		t.Errorf("status = %+v, want non-nil lists", st)
	}
}

func TestFirewallInfoAsksUCIAndTheInterfaces(t *testing.T) {
	show := fixture(t, "firewall-25.12.5.txt")
	m := networkManager{
		run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name == "uci" && strings.Join(args, " ") == "-q show firewall" {
				return []byte(show), nil
			}
			t.Fatalf("unexpected command %s %v", name, args)
			return nil, nil
		},
		lookupInterfaces: func() ([]core.NetworkInterface, error) {
			return []core.NetworkInterface{{Name: "wan", Up: true}}, nil
		},
	}
	st, err := m.FirewallInfo()
	if err != nil {
		t.Fatal(err)
	}
	if !zoneByName(t, st, "wan").Live || zoneByName(t, st, "lan").Live {
		t.Errorf("liveness not taken from the interfaces: %+v", st.Zones)
	}
	if _, err := (networkManager{}).FirewallInfo(); err != core.ErrNotImplemented {
		t.Errorf("without a runner: %v, want ErrNotImplemented", err)
	}
}

// Names are free text in LuCI. A NAT zone called "isp" is the internet side,
// and a zone called "wan" without NAT is not — both fixtures happen to use the
// conventional names, so without this case a name-based role would pass.
func TestTheInternetZoneIsTheOneWithNATWhateverItIsCalled(t *testing.T) {
	show := `firewall.@zone[0]=zone
firewall.@zone[0].name='isp'
firewall.@zone[0].network='pppoe'
firewall.@zone[0].masq='1'
firewall.@zone[1]=zone
firewall.@zone[1].name='wan'
firewall.@zone[1].network='guest'
`
	st := parseFirewall(show, nil)
	if got := zoneByName(t, st, "isp").Role; got != core.ZoneInternet {
		t.Errorf("NAT zone 'isp' role = %q, want internet", got)
	}
	if got := zoneByName(t, st, "wan").Role; got == core.ZoneInternet {
		t.Error("a zone called 'wan' without NAT was taken for the internet side")
	}
}
