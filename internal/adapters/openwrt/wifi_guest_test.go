package openwrt

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/veilbridge-os/veilbridge/internal/adapters/openwrt/ubus"
	"github.com/veilbridge-os/veilbridge/internal/core"
)

// isRefusal matches a refusal of one field, which the API turns into 422.
func isRefusal(field string) func(error) bool {
	return func(err error) bool {
		var fe *core.FieldError
		return errors.As(err, &fe) && fe.Field == field
	}
}

func is(target error) func(error) bool {
	return func(err error) bool { return errors.Is(err, target) }
}

// #58. The router shapes below are the reference router's (25.12.5) as read
// on 10.10.2026: the home network takes the whole delegated /62 with the
// stock ip6assign 60, which is what leaves the guest network without IPv6.

const guestNetworkShow = `network.loopback=interface
network.loopback.device='lo'
network.globals=globals
network.globals.ula_prefix='fd12:3456:789a::/48'
network.@device[0]=device
network.@device[0].name='br-lan'
network.@device[0].type='bridge'
network.lan=interface
network.lan.device='br-lan'
network.lan.proto='static'
network.lan.ipaddr='192.168.1.1'
network.lan.ip6assign='60'
network.wan=interface
network.wan.device='wan'
network.wan.proto='dhcp'
network.wan6=interface
network.wan6.device='wan'
network.wan6.proto='dhcpv6'
`

const guestFirewallShow = `firewall.@zone[0]=zone
firewall.@zone[0].name='lan'
firewall.@zone[0].network='lan'
firewall.@zone[1]=zone
firewall.@zone[1].name='wan'
firewall.@zone[1].network='wan' 'wan6'
firewall.@zone[1].masq='1'
`

// The uplink sits in 192.168.3.0/24 here on purpose: the guest network must
// not take a network the router already has.
const guestIfaceDump = `{"interface":[
{"interface":"lan","up":true,"ipv4-address":[{"address":"192.168.1.1","mask":24}],"route":[]},
{"interface":"wan","up":true,"ipv4-address":[{"address":"192.168.3.20","mask":24}],
 "route":[{"target":"0.0.0.0","mask":0,"nexthop":"192.168.3.1"}]},
{"interface":"wan6","up":true,"ipv6-prefix":[{"address":"2001:db8:0:c::","mask":62}],"route":[]}
]}`

// ourGuest is the panel's own guest network on both radios, as StageGuest
// writes it (the parts the readers look at).
const ourGuestNetwork = `network.guest_dev=device
network.guest_dev.name='br-guest'
network.guest_dev.type='bridge'
network.guest=interface
network.guest.device='br-guest'
network.guest.proto='static'
`

const ourGuestWireless = `wireless.guest_radio0=wifi-iface
wireless.guest_radio0.device='radio0'
wireless.guest_radio0.mode='ap'
wireless.guest_radio0.network='guest'
wireless.guest_radio0.ssid='Guests'
wireless.guest_radio0.encryption='sae-mixed'
wireless.guest_radio0.key='guest-secret-1'
wireless.guest_radio1=wifi-iface
wireless.guest_radio1.device='radio1'
wireless.guest_radio1.mode='ap'
wireless.guest_radio1.network='guest'
wireless.guest_radio1.ssid='Guests'
wireless.guest_radio1.encryption='sae-mixed'
wireless.guest_radio1.key='guest-secret-1'
`

func newGuestFixture(t *testing.T, network, firewall, extraWireless string) *wifiFixture {
	t.Helper()
	f := newWiFiFixture(t)
	f.r.out["uci -q show network"] = []byte(network)
	f.r.out["uci -q show firewall"] = []byte(firewall)
	if extraWireless != "" {
		f.r.out["uci -q show wireless"] = append(f.r.out["uci -q show wireless"], []byte(extraWireless)...)
	}
	f.r.out["/bin/ubus call network.interface dump"] = []byte(guestIfaceDump)
	f.m.readFile = func(path string) ([]byte, error) {
		if path != netifdWirelessScript {
			return nil, errors.New("unexpected read " + path)
		}
		return []byte("\tconfig_add_boolean bridge_isolate\n"), nil
	}
	// The files on disk read as the draft unless a test says otherwise:
	// `uci -c <dir> show veilbridge_committed_<config>` answers what
	// `uci show <config>` does, under the alias.
	inner := f.m.net.run
	f.m.net.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "uci" && len(args) == 5 && args[1] == "-c" && args[3] == "show" {
			cfg := strings.TrimPrefix(args[4], committedPrefix)
			out, err := inner(ctx, "uci", "-q", "show", cfg)
			return []byte(strings.ReplaceAll(string(out), cfg+".", args[4]+".")), err
		}
		return inner(ctx, name, args...)
	}
	return f
}

func TestGuestStatusNoneNeedsTheHomeNetworkRenumbered(t *testing.T) {
	st := newGuestFixture(t, guestNetworkShow, guestFirewallShow, "").status()
	if st.Guest == nil {
		t.Fatal("guest = nil on a router with radios and bridge_isolate")
	}
	want := core.WiFiGuest{State: core.GuestNone, IPv6: true, RenumbersHome: true}
	if *st.Guest != want {
		t.Errorf("guest = %+v, want %+v", *st.Guest, want)
	}
	for _, n := range st.Networks {
		if n.Kind == core.WiFiKindGuest || !n.Enabled {
			t.Errorf("network %+v", n)
		}
	}
}

func TestGuestIPv6RoomInALargerDelegatedPrefix(t *testing.T) {
	network := parseUCISections(guestNetworkShow)
	cases := []struct {
		name           string
		prefixes       []int // delegated prefix lengths
		show           []uciSection
		ipv6, renumber bool
	}{
		{"delegated /62, home /60", []int{62}, network, true, true},
		{"delegated /56, home /60", []int{56}, network, true, false},
		{"delegated /60, home /60: the home takes all of it", []int{60}, network, true, true},
		{"no prefix at all, home /60", nil, network, true, true}, // ULA only: measured nothing for guests
		{"home already /64", []int{62}, parseUCISections(strings.Replace(guestNetworkShow, "ip6assign='60'", "ip6assign='64'", 1)), true, false},
		{"no IPv6 anywhere", nil, parseUCISections(strings.Replace(guestNetworkShow, "network.globals.ula_prefix='fd12:3456:789a::/48'\n", "", 1)), false, false},
	}
	for _, tc := range cases {
		var p []ubus.Address
		for _, m := range tc.prefixes {
			p = append(p, ubus.Address{Address: "2001:db8::", Mask: m})
		}
		ipv6, renumber := guestIPv6(tc.show, p)
		if ipv6 != tc.ipv6 || renumber != tc.renumber {
			t.Errorf("%s: ipv6=%v renumber=%v, want %v %v", tc.name, ipv6, renumber, tc.ipv6, tc.renumber)
		}
	}
}

func TestStageGuestWritesTheMeasuredRecipe(t *testing.T) {
	f := newGuestFixture(t, guestNetworkShow, guestFirewallShow, "")
	changes, err := f.m.StageGuest(core.GuestConfig{SSID: "Guests", Password: "guest-secret-1"})
	if err != nil {
		t.Fatal(err)
	}
	var lists []string
	for _, c := range f.r.calls {
		if s, ok := strings.CutPrefix(c, "uci add_list "); ok {
			lists = append(lists, s)
		}
	}
	want := []string{
		"network.guest_dev=device", "network.guest_dev.name=br-guest", "network.guest_dev.type=bridge",
		"network.guest_dev.bridge_empty=1",
		"network.guest=interface", "network.guest.device=br-guest", "network.guest.proto=static",
		// 192.168.3.0/24 is the uplink's here
		"network.guest.ipaddr=192.168.4.1", "network.guest.netmask=255.255.255.0", "network.guest.ip6assign=64",
		"network.lan.ip6assign=64",
		"wireless.guest_radio0=wifi-iface", "wireless.guest_radio0.device=radio0", "wireless.guest_radio0.mode=ap",
		"wireless.guest_radio0.network=guest", "wireless.guest_radio0.ssid=Guests",
		"wireless.guest_radio0.encryption=sae-mixed", "wireless.guest_radio0.key=guest-secret-1",
		"wireless.guest_radio0.isolate=1", "wireless.guest_radio0.bridge_isolate=1",
		"wireless.guest_radio1=wifi-iface", "wireless.guest_radio1.device=radio1", "wireless.guest_radio1.mode=ap",
		"wireless.guest_radio1.network=guest", "wireless.guest_radio1.ssid=Guests",
		"wireless.guest_radio1.encryption=sae-mixed", "wireless.guest_radio1.key=guest-secret-1",
		"wireless.guest_radio1.isolate=1", "wireless.guest_radio1.bridge_isolate=1",
		"dhcp.guest=dhcp", "dhcp.guest.interface=guest", "dhcp.guest.start=100", "dhcp.guest.limit=150",
		"dhcp.guest.leasetime=2h", "dhcp.guest.dhcpv4=server", "dhcp.guest.dhcpv6=server", "dhcp.guest.ra=server",
		"dhcp.guest.ra_slaac=1",
		"firewall.guest=zone", "firewall.guest.name=guest", "firewall.guest.network=guest",
		"firewall.guest.input=REJECT", "firewall.guest.output=ACCEPT", "firewall.guest.forward=REJECT",
		"firewall.guest_wan=forwarding", "firewall.guest_wan.src=guest", "firewall.guest_wan.dest=wan",
		"firewall.guest_dhcp=rule", "firewall.guest_dhcp.name=Guest DHCP", "firewall.guest_dhcp.src=guest",
		"firewall.guest_dhcp.proto=udp", "firewall.guest_dhcp.dest_port=67-68", "firewall.guest_dhcp.target=ACCEPT",
		"firewall.guest_dns=rule", "firewall.guest_dns.name=Guest DNS", "firewall.guest_dns.src=guest",
		"firewall.guest_dns.proto=tcp udp", "firewall.guest_dns.dest_port=53", "firewall.guest_dns.target=ACCEPT",
		"firewall.guest_icmpv6=rule", "firewall.guest_icmpv6.name=Guest ICMPv6", "firewall.guest_icmpv6.src=guest",
		"firewall.guest_icmpv6.proto=icmp", "firewall.guest_icmpv6.family=ipv6", "firewall.guest_icmpv6.target=ACCEPT",
		"firewall.guest_dhcpv6=rule", "firewall.guest_dhcpv6.name=Guest DHCPv6", "firewall.guest_dhcpv6.src=guest",
		"firewall.guest_dhcpv6.proto=udp", "firewall.guest_dhcpv6.family=ipv6", "firewall.guest_dhcpv6.dest_port=547",
		"firewall.guest_dhcpv6.target=ACCEPT",
		"firewall.guest_no_private4=rule", "firewall.guest_no_private4.name=Guest no private IPv4",
		"firewall.guest_no_private4.src=guest", "firewall.guest_no_private4.dest=wan",
		"firewall.guest_no_private4.family=ipv4", "firewall.guest_no_private4.proto=all",
		"firewall.guest_no_private4.target=REJECT",
		"firewall.guest_no_private6=rule", "firewall.guest_no_private6.name=Guest no private IPv6",
		"firewall.guest_no_private6.src=guest", "firewall.guest_no_private6.dest=wan",
		"firewall.guest_no_private6.family=ipv6", "firewall.guest_no_private6.proto=all",
		"firewall.guest_no_private6.target=REJECT",
	}
	if got := f.sets(); !slices.Equal(got, want) {
		t.Errorf("sets:\n got %q\nwant %q", got, want)
	}
	wantLists := []string{
		"firewall.guest_icmpv6.icmp_type=router-solicitation",
		"firewall.guest_icmpv6.icmp_type=neighbour-solicitation",
		"firewall.guest_icmpv6.icmp_type=neighbour-advertisement",
		"firewall.guest_no_private4.dest_ip=10.0.0.0/8", "firewall.guest_no_private4.dest_ip=172.16.0.0/12",
		"firewall.guest_no_private4.dest_ip=192.168.0.0/16", "firewall.guest_no_private4.dest_ip=100.64.0.0/10",
		"firewall.guest_no_private4.dest_ip=169.254.0.0/16",
		"firewall.guest_no_private6.dest_ip=fc00::/7",
	}
	if !slices.Equal(lists, wantLists) {
		t.Errorf("lists:\n got %q\nwant %q", lists, wantLists)
	}
	if len(changes) != 2 {
		t.Fatalf("changes = %+v", changes)
	}
	g := changes[0]
	if g.LabelKey != "wireless.guest.section" || g.From != "" || g.To != "on|2.4,5|Guests" || !g.Dangerous {
		t.Errorf("guest row = %+v", g)
	}
	l := changes[1]
	if l.LabelKey != "network.lan.ip6assign" || l.From != "60" || l.To != "64" {
		t.Errorf("IPv6 row = %+v", l)
	}
	for _, c := range changes {
		if strings.Contains(c.From+c.To, "guest-secret") {
			t.Errorf("the password is in a row: %+v", c)
		}
	}
}

func TestStageGuestRefuses(t *testing.T) {
	cases := []struct {
		name    string
		network string
		cfg     core.GuestConfig
		noIso   bool
		want    func(error) bool
	}{
		{"empty password: wifi-scripts would take it silently", guestNetworkShow,
			core.GuestConfig{SSID: "Guests"}, false, isRefusal("password")},
		{"no name", guestNetworkShow, core.GuestConfig{Password: "guest-secret-1"}, false, isRefusal("ssid")},
		{"open network is not offered", guestNetworkShow,
			core.GuestConfig{SSID: "Guests", Password: "guest-secret-1", Security: "open"}, false, isRefusal("security")},
		{"unknown radio", guestNetworkShow,
			core.GuestConfig{SSID: "Guests", Password: "guest-secret-1", Radios: []string{"radio9"}}, false, isRefusal("radios")},
		{"a guest network made by hand", guestNetworkShow + "network.guest=interface\nnetwork.guest.proto='static'\n",
			core.GuestConfig{SSID: "Guests", Password: "guest-secret-1"}, false, is(core.ErrGuestExists)},
		{"the panel's own already", guestNetworkShow + ourGuestNetwork,
			core.GuestConfig{SSID: "Guests", Password: "guest-secret-1"}, false, is(core.ErrGuestExists)},
		{"netifd cannot isolate bridge ports", guestNetworkShow,
			core.GuestConfig{SSID: "Guests", Password: "guest-secret-1"}, true, is(core.ErrNotImplemented)},
	}
	for _, tc := range cases {
		f := newGuestFixture(t, tc.network, guestFirewallShow, "")
		if tc.noIso {
			f.m.readFile = func(string) ([]byte, error) { return []byte("config_add_boolean isolate\n"), nil }
		}
		_, err := f.m.StageGuest(tc.cfg)
		if !tc.want(err) {
			t.Errorf("%s: err = %v", tc.name, err)
		}
		if s := f.sets(); len(s) != 0 {
			t.Errorf("%s: staged %q", tc.name, s)
		}
	}
}

func TestGuestStatusOursAndForeign(t *testing.T) {
	st := newGuestFixture(t, guestNetworkShow+ourGuestNetwork, guestFirewallShow, ourGuestWireless).status()
	if st.Guest == nil || st.Guest.State != core.GuestOn || st.Guest.Network != "guest_radio0" || st.Guest.RenumbersHome {
		t.Errorf("guest = %+v", st.Guest)
	}
	var guest *core.WiFiNetwork
	for i, n := range st.Networks {
		if n.Kind == core.WiFiKindGuest {
			guest = &st.Networks[i]
		}
	}
	if guest == nil || guest.SSID != "Guests" || !slices.Equal(guest.Radios, []string{"radio0", "radio1"}) || !guest.Enabled {
		t.Errorf("guest network = %+v", guest)
	}

	off := strings.ReplaceAll(ourGuestWireless, "wireless.guest_radio0.mode='ap'\n", "wireless.guest_radio0.mode='ap'\nwireless.guest_radio0.disabled='1'\n")
	off = strings.ReplaceAll(off, "wireless.guest_radio1.mode='ap'\n", "wireless.guest_radio1.mode='ap'\nwireless.guest_radio1.disabled='1'\n")
	if st := newGuestFixture(t, guestNetworkShow+ourGuestNetwork, guestFirewallShow, off).status(); st.Guest.State != core.GuestOff {
		t.Errorf("all access points off: guest = %+v", st.Guest)
	}

	// A zone called guest made in LuCI: the panel shows it as it is and does
	// not offer to make another.
	fw := guestFirewallShow + "firewall.@zone[2]=zone\nfirewall.@zone[2].name='guest'\n"
	st = newGuestFixture(t, guestNetworkShow, fw, "").status()
	if st.Guest.State != core.GuestForeign {
		t.Errorf("hand-made zone: guest = %+v", st.Guest)
	}
}

func TestStageGuestEnabledAndRemove(t *testing.T) {
	f := newGuestFixture(t, guestNetworkShow+ourGuestNetwork, guestFirewallShow+"firewall.guest=zone\nfirewall.guest.name='guest'\nfirewall.guest_wan=forwarding\nfirewall.guest_dns=rule\n", ourGuestWireless)
	changes, err := f.m.StageGuestEnabled(false)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.sets(); !slices.Equal(got, []string{"wireless.guest_radio0.disabled=1", "wireless.guest_radio1.disabled=1"}) {
		t.Errorf("off: %q", got)
	}
	if len(changes) != 1 || changes[0].From != "on|2.4,5|Guests" || changes[0].To != "off|2.4,5|Guests" {
		t.Errorf("off rows = %+v", changes)
	}

	f = newGuestFixture(t, guestNetworkShow+ourGuestNetwork, guestFirewallShow+"firewall.guest=zone\nfirewall.guest.name='guest'\nfirewall.guest_wan=forwarding\nfirewall.guest_dns=rule\n", ourGuestWireless)
	f.r.out["uci -q show dhcp"] = []byte("dhcp.guest=dhcp\ndhcp.guest.interface='guest'\n")
	changes, err = f.m.StageGuestRemove()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-wireless.guest_radio0", "-wireless.guest_radio1", "-network.guest", "-network.guest_dev",
		"-dhcp.guest", "-firewall.guest", "-firewall.guest_wan", "-firewall.guest_dns"}
	if got := f.sets(); !slices.Equal(got, want) {
		t.Errorf("remove:\n got %q\nwant %q", got, want)
	}
	if len(changes) != 1 || changes[0].From != "on|2.4,5|Guests" || changes[0].To != "" {
		t.Errorf("remove rows = %+v", changes)
	}
	for _, c := range f.r.calls {
		if strings.Contains(c, "lan.ip6assign") {
			t.Errorf("removal touched the home network's IPv6: %q", c)
		}
	}

	f = newGuestFixture(t, guestNetworkShow, guestFirewallShow, "")
	if _, err := f.m.StageGuestEnabled(true); !errors.Is(err, core.ErrNoGuest) {
		t.Errorf("enable without a guest: %v", err)
	}
	if _, err := f.m.StageGuestRemove(); !errors.Is(err, core.ErrNoGuest) {
		t.Errorf("remove without a guest: %v", err)
	}
}

// After a reload the draft is read back from the device: the dozen guest
// sections must still be ONE row, worded as StageGuest worded it.
func TestStagedChangesFoldTheGuestNetworkIntoOneRow(t *testing.T) {
	staged := map[string]string{
		"network":  guestNetworkShow + ourGuestNetwork,
		"wireless": string(readFixture(t, "wireless-25.12.5.uci")) + ourGuestWireless,
		"firewall": guestFirewallShow + "firewall.guest=zone\nfirewall.guest.name='guest'\n",
	}
	committed := map[string]string{
		"network":  guestNetworkShow,
		"wireless": string(readFixture(t, "wireless-25.12.5.uci")),
		"firewall": guestFirewallShow,
	}
	changesOut := strings.Join([]string{
		"network.guest_dev='device'", "network.guest_dev.name='br-guest'",
		"network.guest='interface'", "network.guest.device='br-guest'",
		"wireless.guest_radio0='wifi-iface'", "wireless.guest_radio0.ssid='Guests'",
		"wireless.guest_radio0.key='guest-secret-1'",
		"wireless.guest_radio1='wifi-iface'", "wireless.guest_radio1.ssid='Guests'",
		"firewall.guest='zone'", "firewall.guest_icmpv6.icmp_type+='router-solicitation'",
	}, "\n")
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		line := name + " " + strings.Join(args, " ")
		switch {
		case line == "uci changes":
			return []byte(changesOut), nil
		case len(args) == 3 && args[0] == "-q" && args[1] == "show":
			return []byte(staged[args[2]]), nil
		case len(args) == 5 && args[1] == "-c" && args[3] == "show":
			cfg := strings.TrimPrefix(args[4], committedPrefix)
			return []byte(strings.ReplaceAll(committed[cfg], cfg+".", args[4]+".")), nil
		}
		return nil, errors.New("uci: Entry not found")
	}
	m := networkManager{run: run, configDir: t.TempDir()}
	changes, err := m.StagedChanges()
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 {
		t.Fatalf("changes = %+v", changes)
	}
	if c := changes[0]; c.LabelKey != "wireless.guest.section" || c.From != "" || c.To != "on|2.4,5|Guests" || !c.Dangerous {
		t.Errorf("row = %+v", c)
	}
}

func TestFreeGuestSubnetSkipsEverythingTheRouterHas(t *testing.T) {
	cidr, err := freeGuestSubnet(nil, nil)
	if err != nil || cidr != "192.168.3.0/24" {
		t.Errorf("empty router: %q %v", cidr, err)
	}
	// A route only the kernel has (a VPN's) is taken too (review of #58).
	cidr, err = freeGuestSubnet(nil, []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0"), netip.MustParsePrefix("192.168.3.0/24")})
	if err != nil || cidr != "192.168.4.0/24" {
		t.Errorf("kernel route on 192.168.3.0/24: %q %v", cidr, err)
	}
}

func TestStageGuestRemoveSkipsSectionsRemovedByHand(t *testing.T) {
	// The zone and the DHCP section were removed in LuCI: the panel still
	// removes the rest instead of failing on what is not there.
	f := newGuestFixture(t, guestNetworkShow+ourGuestNetwork, guestFirewallShow+"firewall.guest_wan=forwarding\n", ourGuestWireless)
	if _, err := f.m.StageGuestRemove(); err != nil {
		t.Fatal(err)
	}
	want := []string{"-wireless.guest_radio0", "-wireless.guest_radio1", "-network.guest", "-network.guest_dev", "-firewall.guest_wan"}
	if got := f.sets(); !slices.Equal(got, want) {
		t.Errorf("remove:\n got %q\nwant %q", got, want)
	}
}

func TestStageGuestNamesEachRadioOnce(t *testing.T) {
	f := newGuestFixture(t, guestNetworkShow, guestFirewallShow, "")
	if _, err := f.m.StageGuest(core.GuestConfig{SSID: "Guests", Password: "guest-secret-1", Radios: []string{"radio1", "radio1"}}); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, s := range f.sets() {
		if strings.HasPrefix(s, "wireless.guest_") && strings.HasSuffix(s, "=wifi-iface") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("access points staged = %d, want 1", n)
	}
}

// stagedRows runs StagedChanges over a draft given as `uci show` text per
// configuration, on disk and in the draft, and the `uci changes` lines.
func stagedRows(t *testing.T, committed, staged map[string]string, lines ...string) []core.ConfigChange {
	t.Helper()
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		line := name + " " + strings.Join(args, " ")
		switch {
		case line == "uci changes":
			return []byte(strings.Join(lines, "\n")), nil
		case len(args) == 3 && args[0] == "-q" && args[1] == "show":
			return []byte(staged[args[2]]), nil
		case len(args) == 5 && args[1] == "-c" && args[3] == "show":
			cfg := strings.TrimPrefix(args[4], committedPrefix)
			return []byte(strings.ReplaceAll(committed[cfg], cfg+".", args[4]+".")), nil
		}
		return nil, errors.New("uci: Entry not found")
	}
	m := networkManager{run: run, configDir: t.TempDir()}
	rows, err := m.StagedChanges()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// Review of #58: a guest rule removed on the firewall screen while the guest
// network stays is a firewall change of its own. Folded into the guest row
// (which does not change) it vanished from the apply bar.
func TestStagedChangesKeepAnEditToOneGuestRule(t *testing.T) {
	wl := string(readFixture(t, "wireless-25.12.5.uci")) + ourGuestWireless
	nw := guestNetworkShow + ourGuestNetwork
	fwBefore := guestFirewallShow + "firewall.guest=zone\nfirewall.guest.name='guest'\nfirewall.guest_dns=rule\nfirewall.guest_dns.name='Guest DNS'\nfirewall.guest_dns.src='guest'\nfirewall.guest_dns.target='ACCEPT'\n"
	fwAfter := guestFirewallShow + "firewall.guest=zone\nfirewall.guest.name='guest'\n"
	rows := stagedRows(t,
		map[string]string{"network": nw, "wireless": wl, "firewall": fwBefore},
		map[string]string{"network": nw, "wireless": wl, "firewall": fwAfter},
		"-firewall.guest_dns")
	if len(rows) != 1 || rows[0].LabelKey == "wireless.guest.section" || rows[0].Detail != "firewall.guest_dns" {
		t.Errorf("rows = %+v, want the removed rule's own row", rows)
	}
}

func TestStagedChangesFoldTurningTheGuestNetworkOff(t *testing.T) {
	wl := string(readFixture(t, "wireless-25.12.5.uci")) + ourGuestWireless
	off := strings.ReplaceAll(wl, "wireless.guest_radio0.mode='ap'\n", "wireless.guest_radio0.mode='ap'\nwireless.guest_radio0.disabled='1'\n")
	off = strings.ReplaceAll(off, "wireless.guest_radio1.mode='ap'\n", "wireless.guest_radio1.mode='ap'\nwireless.guest_radio1.disabled='1'\n")
	nw := guestNetworkShow + ourGuestNetwork
	rows := stagedRows(t,
		map[string]string{"network": nw, "wireless": wl, "firewall": guestFirewallShow},
		map[string]string{"network": nw, "wireless": off, "firewall": guestFirewallShow},
		"wireless.guest_radio0.disabled='1'", "wireless.guest_radio1.disabled='1'")
	if len(rows) != 1 || rows[0].From != "on|2.4,5|Guests" || rows[0].To != "off|2.4,5|Guests" {
		t.Errorf("rows = %+v", rows)
	}
}

// Review of #58: with the home network in the uplink's zone (one of our own
// stands, firewall-23.05.5.txt), forwarding guests there would hand them the
// home network.
func TestStageGuestRefusesAnUplinkZoneShared(t *testing.T) {
	fw := "firewall.@zone[0]=zone\nfirewall.@zone[0].name='lan'\nfirewall.@zone[0].network='lan' 'wan'\n"
	f := newGuestFixture(t, guestNetworkShow, fw, "")
	_, err := f.m.StageGuest(core.GuestConfig{SSID: "Guests", Password: "guest-secret-1"})
	if !errors.Is(err, core.ErrGuestBlocked) {
		t.Errorf("err = %v, want ErrGuestSharedZone", err)
	}
	if s := f.sets(); len(s) != 0 {
		t.Errorf("staged %q", s)
	}
}

// Review of #58: without the interface dump the subnet was chosen blind.
func TestStageGuestRefusesWithoutTheInterfaceDump(t *testing.T) {
	f := newGuestFixture(t, guestNetworkShow, guestFirewallShow, "")
	f.r.fail["/bin/ubus call network.interface dump"] = errors.New("ubus: timed out")
	if _, err := f.m.StageGuest(core.GuestConfig{SSID: "Guests", Password: "guest-secret-1"}); err == nil {
		t.Error("staged a guest network without knowing the router's networks")
	}
	if s := f.sets(); len(s) != 0 {
		t.Errorf("staged %q", s)
	}
}

// A failed write takes back what it wrote - its own sections and the home
// network's ip6assign - and nothing else: whole-file reverts would also drop
// other work waiting in the draft (review of #58).
func TestStageGuestRevertsOnlyItsOwnKeys(t *testing.T) {
	f := newGuestFixture(t, guestNetworkShow, guestFirewallShow, "")
	f.r.fail["uci set firewall.guest_dns="] = errors.New("uci: I/O error")
	if _, err := f.m.StageGuest(core.GuestConfig{SSID: "Guests", Password: "guest-secret-1"}); err == nil {
		t.Fatal("no error")
	}
	for _, k := range []string{"network.guest_dev", "network.guest", "network.lan.ip6assign",
		"wireless.guest_radio0", "wireless.guest_radio1", "dhcp.guest", "firewall.guest", "firewall.guest_dns"} {
		if !slices.Contains(f.r.calls, "uci revert "+k) {
			t.Errorf("%s not reverted", k)
		}
	}
	for _, c := range []string{"network", "wireless", "dhcp", "firewall"} {
		if slices.Contains(f.r.calls, "uci revert "+c) {
			t.Errorf("the whole %s draft was reverted", c)
		}
	}
}

// A hand-made access point under the panel's name is not taken over: the
// guest network reads as made by hand (review of #58).
func TestGuestSectionNameTakenByHand(t *testing.T) {
	wl := "wireless.guest_radio0=wifi-iface\nwireless.guest_radio0.device='radio0'\nwireless.guest_radio0.mode='sta'\nwireless.guest_radio0.network='wwan'\n"
	f := newGuestFixture(t, guestNetworkShow, guestFirewallShow, wl)
	if st := f.status(); st.Guest.State != core.GuestForeign {
		t.Errorf("guest = %+v, want foreign", st.Guest)
	}
	f = newGuestFixture(t, guestNetworkShow, guestFirewallShow, wl)
	if _, err := f.m.StageGuest(core.GuestConfig{SSID: "Guests", Password: "guest-secret-1"}); !errors.Is(err, core.ErrGuestExists) {
		t.Errorf("err = %v, want ErrGuestExists", err)
	}
	if s := f.sets(); len(s) != 0 {
		t.Errorf("staged %q", s)
	}
}

// Review of #58, measured on the reference router: fw4 runs a rule from every
// zone before the zones, so the guests' zone cannot stop it. With one that
// accepts, no guest network is made, and the status says which rule.
func TestGuestBlockedByARuleFromEveryZone(t *testing.T) {
	fw := guestFirewallShow + "firewall.@rule[0]=rule\nfirewall.@rule[0].name='Any to LAN'\nfirewall.@rule[0].src='*'\nfirewall.@rule[0].dest='lan'\nfirewall.@rule[0].target='ACCEPT'\n"
	f := newGuestFixture(t, guestNetworkShow, fw, "")
	st := f.status()
	if st.Guest.Blocked != core.GuestBlockedOpenRule || st.Guest.BlockedBy != "Any to LAN" {
		t.Errorf("guest = %+v", st.Guest)
	}
	f = newGuestFixture(t, guestNetworkShow, fw, "")
	if _, err := f.m.StageGuest(core.GuestConfig{SSID: "Guests", Password: "guest-secret-1"}); !errors.Is(err, core.ErrGuestBlocked) {
		t.Errorf("err = %v", err)
	}
	if s := f.sets(); len(s) != 0 {
		t.Errorf("staged %q", s)
	}
	// A rule from every zone that blocks, or one turned off, is no hole.
	for _, ok := range []string{
		strings.Replace(fw, "target='ACCEPT'", "target='REJECT'", 1),
		fw + "firewall.@rule[0].enabled='0'\n",
	} {
		if st := newGuestFixture(t, guestNetworkShow, ok, "").status(); st.Guest.Blocked != "" {
			t.Errorf("blocked = %q for %q", st.Guest.Blocked, ok)
		}
	}
}

func TestGuestStatusBlockedByASharedZone(t *testing.T) {
	fw := "firewall.@zone[0]=zone\nfirewall.@zone[0].name='lan'\nfirewall.@zone[0].network='lan' 'wan'\n"
	st := newGuestFixture(t, guestNetworkShow, fw, "").status()
	if st.Guest.Blocked != core.GuestBlockedSharedZone || st.Guest.BlockedBy != "lan" {
		t.Errorf("guest = %+v", st.Guest)
	}
}

// Review of #58: an unreadable dhcp is not an empty one.
func TestStageGuestRefusesWithoutTheDHCPConfig(t *testing.T) {
	f := newGuestFixture(t, guestNetworkShow, guestFirewallShow, "")
	f.r.fail["uci -q show dhcp"] = errors.New("uci: I/O error")
	if _, err := f.m.StageGuest(core.GuestConfig{SSID: "Guests", Password: "guest-secret-1"}); err == nil {
		t.Error("staged without knowing whether dhcp.guest is taken")
	}
	if s := f.sets(); len(s) != 0 {
		t.Errorf("staged %q", s)
	}
}

// The panel's guest network with every protection in place, as StageGuest
// makes it (the parts guestWeakened reads).
const ourGuestFirewall = guestFirewallShow + `firewall.guest=zone
firewall.guest.name='guest'
firewall.guest.network='guest'
firewall.guest.input='REJECT'
firewall.guest.forward='REJECT'
firewall.guest_wan=forwarding
firewall.guest_wan.src='guest'
firewall.guest_wan.dest='wan'
firewall.guest_dhcp=rule
firewall.guest_dhcp.name='Guest DHCP'
firewall.guest_dhcp.src='guest'
firewall.guest_dhcp.proto='udp'
firewall.guest_dhcp.dest_port='67-68'
firewall.guest_dhcp.target='ACCEPT'
firewall.guest_dns=rule
firewall.guest_dns.name='Guest DNS'
firewall.guest_dns.src='guest'
firewall.guest_dns.proto='tcp udp'
firewall.guest_dns.dest_port='53'
firewall.guest_dns.target='ACCEPT'
firewall.guest_icmpv6=rule
firewall.guest_icmpv6.name='Guest ICMPv6'
firewall.guest_icmpv6.src='guest'
firewall.guest_icmpv6.proto='icmp'
firewall.guest_icmpv6.family='ipv6'
firewall.guest_icmpv6.target='ACCEPT'
firewall.guest_icmpv6.icmp_type='router-solicitation' 'neighbour-solicitation' 'neighbour-advertisement'
firewall.guest_dhcpv6=rule
firewall.guest_dhcpv6.name='Guest DHCPv6'
firewall.guest_dhcpv6.src='guest'
firewall.guest_dhcpv6.proto='udp'
firewall.guest_dhcpv6.family='ipv6'
firewall.guest_dhcpv6.dest_port='547'
firewall.guest_dhcpv6.target='ACCEPT'
firewall.guest_no_private4=rule
firewall.guest_no_private4.name='Guest no private IPv4'
firewall.guest_no_private4.src='guest'
firewall.guest_no_private4.dest='wan'
firewall.guest_no_private4.family='ipv4'
firewall.guest_no_private4.proto='all'
firewall.guest_no_private4.target='REJECT'
firewall.guest_no_private4.dest_ip='10.0.0.0/8' '172.16.0.0/12' '192.168.0.0/16' '100.64.0.0/10' '169.254.0.0/16'
firewall.guest_no_private6=rule
firewall.guest_no_private6.name='Guest no private IPv6'
firewall.guest_no_private6.src='guest'
firewall.guest_no_private6.dest='wan'
firewall.guest_no_private6.family='ipv6'
firewall.guest_no_private6.proto='all'
firewall.guest_no_private6.target='REJECT'
firewall.guest_no_private6.dest_ip='fc00::/7'
`

// withoutSection drops every line of one section from `uci show` text.
func withoutSection(show, section string) string {
	var keep []string
	for _, l := range strings.SplitAfter(show, "\n") {
		if !strings.HasPrefix(l, section+"=") && !strings.HasPrefix(l, section+".") {
			keep = append(keep, l)
		}
	}
	return strings.Join(keep, "")
}

var ourIsolatedWireless = strings.NewReplacer(
	"wireless.guest_radio0.mode='ap'\n", "wireless.guest_radio0.mode='ap'\nwireless.guest_radio0.isolate='1'\nwireless.guest_radio0.bridge_isolate='1'\n",
	"wireless.guest_radio1.mode='ap'\n", "wireless.guest_radio1.mode='ap'\nwireless.guest_radio1.isolate='1'\nwireless.guest_radio1.bridge_isolate='1'\n",
).Replace(ourGuestWireless)

// Review of #58: a protection of the panel's own guest network changed on the
// firewall screen or in LuCI must not leave the card promising isolation.
func TestGuestStatusNamesAWeakenedProtection(t *testing.T) {
	nw := guestNetworkShow + ourGuestNetwork
	if st := newGuestFixture(t, nw, ourGuestFirewall, ourIsolatedWireless).status(); st.Guest.Blocked != "" {
		t.Fatalf("intact guest network: blocked %q by %q", st.Guest.Blocked, st.Guest.BlockedBy)
	}
	cases := []struct{ fw, wl, want string }{
		{ourGuestFirewall + "firewall.guest_no_private4.enabled='0'\n", ourIsolatedWireless, "firewall.guest_no_private4"},
		{withoutSection(ourGuestFirewall, "firewall.guest_no_private6"), ourIsolatedWireless, "firewall.guest_no_private6"},
		{strings.Replace(ourGuestFirewall, "firewall.guest.input='REJECT'", "firewall.guest.input='ACCEPT'", 1), ourIsolatedWireless, "firewall.guest.input"},
		{strings.Replace(ourGuestFirewall, "firewall.guest.forward='REJECT'", "firewall.guest.forward='ACCEPT'", 1), ourIsolatedWireless, "firewall.guest.forward"},
		{ourGuestFirewall + "firewall.home=forwarding\nfirewall.home.src='guest'\nfirewall.home.dest='lan'\n", ourIsolatedWireless, "firewall.home"},
		{ourGuestFirewall, strings.Replace(ourIsolatedWireless, "wireless.guest_radio1.bridge_isolate='1'", "wireless.guest_radio1.bridge_isolate='0'", 1), "wireless.guest_radio1.bridge_isolate"},
		{ourGuestFirewall, strings.Replace(ourIsolatedWireless, "wireless.guest_radio0.isolate='1'", "wireless.guest_radio0.isolate='0'", 1), "wireless.guest_radio0.isolate"},
		// Review of #58, round 5: rules that let guests in, and deny rules
		// that no longer cover what they did.
		{ourGuestFirewall + "firewall.panel=rule\nfirewall.panel.name='Guests to the panel'\nfirewall.panel.src='guest'\nfirewall.panel.proto='tcp'\nfirewall.panel.dest_port='8080'\nfirewall.panel.target='ACCEPT'\n", ourIsolatedWireless, "firewall.Guests to the panel"},
		{strings.Replace(ourGuestFirewall, "firewall.guest_dns.dest_port='53'", "firewall.guest_dns.dest_port='53 8080'", 1), ourIsolatedWireless, "firewall.guest_dns"},
		{strings.Replace(ourGuestFirewall, "firewall.guest_no_private4.src='guest'", "firewall.guest_no_private4.src='lan'", 1), ourIsolatedWireless, "firewall.guest_no_private4"},
		{strings.Replace(ourGuestFirewall, " '192.168.0.0/16'", "", 1), ourIsolatedWireless, "firewall.guest_no_private4"},
		{ourGuestFirewall + "firewall.guest_no_private4.src_ip='192.168.3.50'\n", ourIsolatedWireless, "firewall.guest_no_private4"},
		{ourGuestFirewall + "firewall.fwd=redirect\nfirewall.fwd.src='guest'\nfirewall.fwd.dest='lan'\n", ourIsolatedWireless, "firewall.fwd"},
		// Round 8: an empty option must not stand in for a missing one, and
		// an access point added to the guest network in LuCI is not isolated.
		{strings.Replace(ourGuestFirewall, "firewall.guest_no_private4.src='guest'", "firewall.guest_no_private4.src_ip=''", 1), ourIsolatedWireless, "firewall.guest_no_private4"},
		// Round 10: a zone narrowed to one address family leaves the other open.
		{strings.Replace(ourGuestFirewall, "firewall.guest.forward='REJECT'\n", "firewall.guest.forward='REJECT'\nfirewall.guest.family='ipv4'\n", 1), ourIsolatedWireless, "firewall.guest.family"},
		{ourGuestFirewall, ourIsolatedWireless + "wireless.@wifi-iface[3]=wifi-iface\nwireless.@wifi-iface[3].device='radio0'\nwireless.@wifi-iface[3].mode='ap'\nwireless.@wifi-iface[3].network='guest'\nwireless.@wifi-iface[3].ssid='Extra'\n", "wireless.Extra"},
	}
	for _, tc := range cases {
		st := newGuestFixture(t, nw, tc.fw, tc.wl).status()
		if st.Guest.Blocked != core.GuestBlockedWeakened || st.Guest.BlockedBy != tc.want {
			t.Errorf("want weakened by %s: blocked %q by %q", tc.want, st.Guest.Blocked, st.Guest.BlockedBy)
		}
	}
}

// Review of #58, round 6.
func TestGuestBlockedByAForwardingLeftFromAnEarlierOne(t *testing.T) {
	fw := guestFirewallShow + "firewall.old=forwarding\nfirewall.old.src='guest'\nfirewall.old.dest='lan'\n"
	st := newGuestFixture(t, guestNetworkShow, fw, "").status()
	if st.Guest.State != core.GuestNone || st.Guest.Blocked != core.GuestBlockedGuestRule || st.Guest.BlockedBy != "old" {
		t.Errorf("guest = %+v", st.Guest)
	}
	f := newGuestFixture(t, guestNetworkShow, fw, "")
	if _, err := f.m.StageGuest(core.GuestConfig{SSID: "Guests", Password: "guest-secret-1"}); !errors.Is(err, core.ErrGuestBlocked) {
		t.Errorf("err = %v", err)
	}
}

func TestGuestWeakenedWhenTheInterfaceLeavesItsZone(t *testing.T) {
	nw := guestNetworkShow + ourGuestNetwork
	moved := strings.Replace(ourGuestFirewall, "firewall.@zone[0].network='lan'", "firewall.@zone[0].network='lan' 'guest'", 1)
	if st := newGuestFixture(t, nw, moved, ourIsolatedWireless).status(); st.Guest.Blocked != core.GuestBlockedWeakened || st.Guest.BlockedBy != "firewall.lan" {
		t.Errorf("guest interface also in lan: %+v", st.Guest)
	}
	emptied := strings.Replace(ourGuestFirewall, "firewall.guest.network='guest'\n", "", 1)
	if st := newGuestFixture(t, nw, emptied, ourIsolatedWireless).status(); st.Guest.BlockedBy != "firewall.guest.network" {
		t.Errorf("guest zone without its interface: %+v", st.Guest)
	}
}

func TestGuestBlockedWithoutAnUplinkZone(t *testing.T) {
	// No default route anywhere and no zone called wan: nowhere to send guests.
	fw := "firewall.@zone[0]=zone\nfirewall.@zone[0].name='lan'\nfirewall.@zone[0].network='lan'\nfirewall.@zone[1]=zone\nfirewall.@zone[1].name='internet'\nfirewall.@zone[1].network='uplink'\n"
	f := newGuestFixture(t, guestNetworkShow, fw, "")
	f.r.out["/bin/ubus call network.interface dump"] = []byte(`{"interface":[{"interface":"lan","up":true,"ipv4-address":[{"address":"192.168.1.1","mask":24}]}]}`)
	if st := f.status(); st.Guest.Blocked != core.GuestBlockedNoUplink {
		t.Errorf("guest = %+v", st.Guest)
	}
	if _, err := f.m.StageGuest(core.GuestConfig{SSID: "Guests", Password: "guest-secret-1"}); !errors.Is(err, core.ErrGuestBlocked) {
		t.Errorf("err = %v", err)
	}
	if s := f.sets(); len(s) != 0 {
		t.Errorf("staged %q", s)
	}
}

// Review of #58, round 7: one of the panel's access points moved to lan in
// LuCI is still the panel's - a weakened protection, and removed with the rest.
func TestGuestAccessPointMovedToTheHomeNetwork(t *testing.T) {
	nw := guestNetworkShow + ourGuestNetwork
	moved := strings.Replace(ourIsolatedWireless, "wireless.guest_radio1.network='guest'", "wireless.guest_radio1.network='lan'", 1)
	st := newGuestFixture(t, nw, ourGuestFirewall, moved).status()
	if st.Guest.Blocked != core.GuestBlockedWeakened || st.Guest.BlockedBy != "wireless.guest_radio1.network" {
		t.Errorf("guest = %+v", st.Guest)
	}
	f := newGuestFixture(t, nw, ourGuestFirewall, moved)
	if _, err := f.m.StageGuestRemove(); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.sets(), "-wireless.guest_radio1") {
		t.Errorf("the moved access point stays: %q", f.sets())
	}
}

// A zone binding the guests' bridge by device (wildcards too) takes guests.
func TestGuestBridgeTakenByAnotherZonesDevice(t *testing.T) {
	for _, dev := range []string{"br-guest", "br-*", "br+"} {
		fw := guestFirewallShow + "firewall.@zone[2]=zone\nfirewall.@zone[2].name='home2'\nfirewall.@zone[2].device='" + dev + "'\n"
		if st := newGuestFixture(t, guestNetworkShow, fw, "").status(); st.Guest.Blocked != core.GuestBlockedGuestRule || st.Guest.BlockedBy != "home2" {
			t.Errorf("device %q before creation: %+v", dev, st.Guest)
		}
		ours := strings.Replace(ourGuestFirewall, "firewall.@zone[1].network='wan' 'wan6'\n",
			"firewall.@zone[1].network='wan' 'wan6'\nfirewall.@zone[2]=zone\nfirewall.@zone[2].name='home2'\nfirewall.@zone[2].device='"+dev+"'\n", 1)
		st := newGuestFixture(t, guestNetworkShow+ourGuestNetwork, ours, ourIsolatedWireless).status()
		if st.Guest.Blocked != core.GuestBlockedWeakened || st.Guest.BlockedBy != "firewall.home2" {
			t.Errorf("device %q with a guest network: %+v", dev, st.Guest)
		}
	}
	// A device that is not the bridge is none of the guests' business.
	fw := guestFirewallShow + "firewall.@zone[2]=zone\nfirewall.@zone[2].name='vpn'\nfirewall.@zone[2].device='wg0'\n"
	if st := newGuestFixture(t, guestNetworkShow, fw, "").status(); st.Guest.Blocked != "" {
		t.Errorf("unrelated device: %+v", st.Guest)
	}
}

// A guest network pending in the draft must not read as none when a read
// fails: StagedChanges fails closed.
func TestStagedChangesFailClosedOnAGuestRead(t *testing.T) {
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		line := name + " " + strings.Join(args, " ")
		switch {
		case line == "uci changes":
			return []byte("network.guest_dev='device'\nnetwork.guest='interface'"), nil
		case line == "uci -q show network":
			return nil, errors.New("uci: I/O error")
		}
		return []byte{}, nil
	}
	m := networkManager{run: run, configDir: t.TempDir()}
	if _, err := m.StagedChanges(); err == nil {
		t.Error("StagedChanges succeeded without reading the guest network")
	}
}

// Review of #58, round 8: guests are not home devices. The internet switch
// and the schedule write rules for the local network's zone and would not
// touch them, so they are not on the device list and the switch refuses.
func TestGuestsAreNotHomeDevices(t *testing.T) {
	sys := cudy(fdbRecord("02:00:5e:00:53:d6", 6, false, 150))
	sys.dirs["/sys/class/net"] = append(sys.dirs["/sys/class/net"], "br-guest", "phy1-ap1")
	sys.files["/sys/class/net/phy1-ap1/phy80211"] = nil
	sys.files["/sys/class/net/br-guest/brif/phy1-ap1"] = nil
	fx := newDevFixture(t, sys,
		"4102444800 02:00:5e:00:53:d6 192.168.1.188 iPhone *\n"+
			"4102444800 02:00:5e:00:53:a1 192.168.4.150 guest-laptop *\n")
	fx.neigh = neighCudy()
	fx.runner.out["/bin/ubus call hostapd.phy1-ap1 get_clients"] = []byte(strings.ReplaceAll(clients5, "02:00:5e:00:53:d6", "02:00:5e:00:53:b2"))
	fx.runner.out["uci -q get network.guest.ipaddr"] = []byte("192.168.4.1\n")
	fx.runner.out["uci -q get network.guest.netmask"] = []byte("255.255.255.0\n")
	got := fx.list()
	if _, ok := got["02:00:5e:00:53:d6"]; !ok {
		t.Errorf("the home phone is gone: %v", got)
	}
	for _, mac := range []string{"02:00:5e:00:53:b2", "02:00:5e:00:53:a1"} {
		if _, ok := got[mac]; ok {
			t.Errorf("guest %s is on the home device list", mac)
		}
	}
	if _, err := fx.m.StageDeviceInternet("02:00:5e:00:53:b2", false); !isRefusal("mac")(err) {
		t.Errorf("turning a guest's internet off: err = %v, want a refusal", err)
	}
	if _, err := fx.m.StageDeviceSchedule("02:00:5e:00:53:a1", &core.InternetSchedule{Days: []string{"mon"}, From: "22:00", To: "07:00"}); !isRefusal("mac")(err) {
		t.Errorf("scheduling a guest: err = %v, want a refusal", err)
	}
}

// Review of #58, round 9.
func TestGuestBlockedWhenTheUplinkZoneBindsTheHomeBridge(t *testing.T) {
	fw := strings.Replace(guestFirewallShow, "firewall.@zone[1].network='wan' 'wan6'\n", "firewall.@zone[1].network='wan' 'wan6'\nfirewall.@zone[1].device='br-lan'\n", 1)
	if st := newGuestFixture(t, guestNetworkShow, fw, "").status(); st.Guest.Blocked != core.GuestBlockedSharedZone {
		t.Errorf("guest = %+v", st.Guest)
	}
}

func TestGuestWeakenedByAPortAddedToItsBridge(t *testing.T) {
	nw := guestNetworkShow + ourGuestNetwork + "network.guest_dev.ports='lan4'\n"
	st := newGuestFixture(t, nw, ourGuestFirewall, ourIsolatedWireless).status()
	if st.Guest.Blocked != core.GuestBlockedWeakened || st.Guest.BlockedBy != "network.guest_dev.ports" {
		t.Errorf("guest = %+v", st.Guest)
	}
}

func TestAHomeDeviceWithAnOldGuestLeaseIsHome(t *testing.T) {
	// The phone is on the home 5 GHz now; its guest lease has not run out.
	fx := newDevFixture(t, cudy(fdbRecord("02:00:5e:00:53:d6", 6, false, 150)),
		"4102444800 02:00:5e:00:53:d6 192.168.4.150 iPhone *\n")
	fx.neigh = neighCudy()
	fx.runner.out["uci -q get network.guest.ipaddr"] = []byte("192.168.4.1\n")
	if _, ok := fx.list()["02:00:5e:00:53:d6"]; !ok {
		t.Error("a phone on the home Wi-Fi left the list for an old guest lease")
	}
	if _, err := fx.m.StageDeviceInternet("02:00:5e:00:53:d6", false); isRefusal("mac")(err) {
		t.Errorf("its internet switch refused: %v", err)
	}
}

// Review of #58, round 10: a device seen at home before and on the guest
// network now does not come back from history as a home device; a device on
// a home cable with an old guest lease keeps its internet switch.
func TestGuestHistoryAndAWiredHomeDevice(t *testing.T) {
	sys := cudy(fdbRecord("02:00:5e:00:53:d6", 6, false, 150))
	sys.dirs["/sys/class/net"] = append(sys.dirs["/sys/class/net"], "br-guest", "phy1-ap1")
	sys.files["/sys/class/net/phy1-ap1/phy80211"] = nil
	sys.files["/sys/class/net/br-guest/brif/phy1-ap1"] = nil
	fx := newDevFixture(t, sys, "")
	fx.neigh = neighCudy()
	fx.m.remember("02:00:5e:00:53:b2", heard{at: fx.clock.Add(-time.Hour), link: core.DeviceLink{Kind: core.LinkWiFi, Band: "5"}})
	fx.runner.out["/bin/ubus call hostapd.phy1-ap1 get_clients"] = []byte(strings.ReplaceAll(clients5, "02:00:5e:00:53:d6", "02:00:5e:00:53:b2"))
	if _, ok := fx.list()["02:00:5e:00:53:b2"]; ok {
		t.Error("a guest came back from history as a home device")
	}

	// On a home socket (port 1, a cable), its guest lease not run out.
	wired := cudy(fdbRecord("02:00:5e:00:53:c3", 1, false, 50))
	fx = newDevFixture(t, wired, "4102444800 02:00:5e:00:53:c3 192.168.4.160 pc *\n")
	fx.neigh = neighCudy()
	fx.runner.out["uci -q get network.guest.ipaddr"] = []byte("192.168.4.1\n")
	if _, ok := fx.list()["02:00:5e:00:53:c3"]; !ok {
		t.Error("a wired home device left the list for an old guest lease")
	}
	if _, err := fx.m.StageDeviceInternet("02:00:5e:00:53:c3", false); isRefusal("mac")(err) {
		t.Errorf("its internet switch refused: %v", err)
	}
}

// Review of #58, round 11.
func TestAnAccessPointLeftOnGuestMakesItForeign(t *testing.T) {
	wl := "wireless.@wifi-iface[3]=wifi-iface\nwireless.@wifi-iface[3].device='radio0'\nwireless.@wifi-iface[3].mode='ap'\nwireless.@wifi-iface[3].network='guest'\n"
	f := newGuestFixture(t, guestNetworkShow, guestFirewallShow, wl)
	if st := f.status(); st.Guest.State != core.GuestForeign {
		t.Errorf("guest = %+v", st.Guest)
	}
	f = newGuestFixture(t, guestNetworkShow, guestFirewallShow, wl)
	if _, err := f.m.StageGuest(core.GuestConfig{SSID: "Guests", Password: "guest-secret-1"}); !errors.Is(err, core.ErrGuestExists) {
		t.Errorf("err = %v", err)
	}
}

func TestTurningGuestsOffTurnsOffAMovedAccessPointToo(t *testing.T) {
	moved := strings.Replace(ourIsolatedWireless, "wireless.guest_radio1.network='guest'", "wireless.guest_radio1.network='lan'", 1)
	f := newGuestFixture(t, guestNetworkShow+ourGuestNetwork, ourGuestFirewall, moved)
	if _, err := f.m.StageGuestEnabled(false); err != nil {
		t.Fatal(err)
	}
	if got := f.sets(); !slices.Equal(got, []string{"wireless.guest_radio0.disabled=1", "wireless.guest_radio1.disabled=1"}) {
		t.Errorf("off: %q", got)
	}
	// On again: only what is still on the guest network.
	off := strings.NewReplacer(
		"wireless.guest_radio0.mode='ap'\n", "wireless.guest_radio0.mode='ap'\nwireless.guest_radio0.disabled='1'\n",
		"wireless.guest_radio1.mode='ap'\n", "wireless.guest_radio1.mode='ap'\nwireless.guest_radio1.disabled='1'\n",
	).Replace(moved)
	f = newGuestFixture(t, guestNetworkShow+ourGuestNetwork, ourGuestFirewall, off)
	if _, err := f.m.StageGuestEnabled(true); err != nil {
		t.Fatal(err)
	}
	if got := f.sets(); !slices.Equal(got, []string{"-wireless.guest_radio0.disabled"}) {
		t.Errorf("on: %q", got)
	}
}

// Review of #58, round 12.
func TestEveryGuestAccessPointMovedHomeCanStillBeTurnedOff(t *testing.T) {
	moved := strings.ReplaceAll(ourIsolatedWireless, "network='guest'", "network='lan'")
	st := newGuestFixture(t, guestNetworkShow+ourGuestNetwork, ourGuestFirewall, moved).status()
	if st.Guest.State != core.GuestOn || st.Guest.Blocked != core.GuestBlockedWeakened {
		t.Errorf("guest = %+v, want on and weakened", st.Guest)
	}
	f := newGuestFixture(t, guestNetworkShow+ourGuestNetwork, ourGuestFirewall, moved)
	if _, err := f.m.StageGuestEnabled(false); err != nil {
		t.Fatal(err)
	}
	if got := f.sets(); !slices.Equal(got, []string{"wireless.guest_radio0.disabled=1", "wireless.guest_radio1.disabled=1"}) {
		t.Errorf("off: %q", got)
	}
}

func TestAFullTunnelVPNDoesNotOccupyEverySubnet(t *testing.T) {
	cidr, err := freeGuestSubnet(nil, []netip.Prefix{netip.MustParsePrefix("0.0.0.0/1"), netip.MustParsePrefix("128.0.0.0/1")})
	if err != nil || cidr != "192.168.3.0/24" {
		t.Errorf("full-tunnel routes: %q %v", cidr, err)
	}
}

func TestADraftOnAWiredRouterReadsWithoutWireless(t *testing.T) {
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		line := name + " " + strings.Join(args, " ")
		switch {
		case line == "uci changes":
			return []byte("firewall.guest_dns.dest_port='5353'"), nil
		case strings.Contains(line, "wireless"):
			return nil, errors.New("uci: Entry not found")
		case len(args) == 3 && args[1] == "show":
			return []byte(guestFirewallShow + "firewall.guest_dns=rule\nfirewall.guest_dns.dest_port='5353'\n"), nil
		}
		return []byte{}, nil
	}
	m := networkManager{run: run, configDir: t.TempDir()} // no wireless file there
	if _, err := m.StagedChanges(); err != nil {
		t.Errorf("a wired router's draft: %v", err)
	}
}

// Review of #58, round 13.
func TestAnAccessPointWithGuestInItsNetworkList(t *testing.T) {
	wl := "wireless.@wifi-iface[3]=wifi-iface\nwireless.@wifi-iface[3].mode='ap'\nwireless.@wifi-iface[3].network='guest' 'secondary'\nwireless.@wifi-iface[3].ssid='Both'\n"
	if st := newGuestFixture(t, guestNetworkShow, guestFirewallShow, wl).status(); st.Guest.State != core.GuestForeign {
		t.Errorf("before creation: %+v", st.Guest)
	}
	st := newGuestFixture(t, guestNetworkShow+ourGuestNetwork, ourGuestFirewall, ourIsolatedWireless+wl).status()
	if st.Guest.Blocked != core.GuestBlockedWeakened || st.Guest.BlockedBy != "wireless.Both" {
		t.Errorf("with a guest network: %+v", st.Guest)
	}
}

func TestTurningOnOneOfTwoAccessPointsSaysOn(t *testing.T) {
	half := strings.Replace(ourIsolatedWireless, "wireless.guest_radio1.mode='ap'\n", "wireless.guest_radio1.mode='ap'\nwireless.guest_radio1.disabled='1'\n", 1)
	f := newGuestFixture(t, guestNetworkShow+ourGuestNetwork, ourGuestFirewall, half)
	rows, err := f.m.StageGuestEnabled(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].From != "1" || rows[0].To != "" || rows[0].Detail != "wireless.guest_radio1.disabled" {
		t.Errorf("rows = %+v, want the one access point going from off to on", rows)
	}
}

// Review of #58, round 14: where guests would get no internet, say so before
// making a guest network that cannot work.
func TestGuestBlockedWithoutNATOrWithASplitUplink(t *testing.T) {
	for _, fw := range []string{
		strings.Replace(guestFirewallShow, "firewall.@zone[1].masq='1'\n", "", 1),
		guestFirewallShow + "firewall.@zone[1].masq_src='192.168.1.0/24'\n",
	} {
		if st := newGuestFixture(t, guestNetworkShow, fw, "").status(); st.Guest.Blocked != core.GuestBlockedNoNAT {
			t.Errorf("no NAT for guests: %+v", st.Guest)
		}
	}
	split := strings.Replace(guestFirewallShow, "firewall.@zone[1].network='wan' 'wan6'\n", "firewall.@zone[1].network='wan'\n", 1) +
		"firewall.@zone[2]=zone\nfirewall.@zone[2].name='wan6zone'\nfirewall.@zone[2].network='wan6'\n"
	if st := newGuestFixture(t, guestNetworkShow, split, "").status(); st.Guest.Blocked != core.GuestBlockedSplitUplink || st.Guest.BlockedBy != "wan6zone" {
		t.Errorf("IPv6 in another zone: %+v", st.Guest)
	}
}

func TestStageGuestRefusesAnUnnamedRadio(t *testing.T) {
	f := newGuestFixture(t, guestNetworkShow, guestFirewallShow, "")
	show := strings.ReplaceAll(string(f.r.out["uci -q show wireless"]), "radio1", "@wifi-device[1]")
	f.r.out["uci -q show wireless"] = []byte(show)
	_, err := f.m.StageGuest(core.GuestConfig{SSID: "Guests", Password: "guest-secret-1", Radios: []string{"@wifi-device[1]"}})
	if !isRefusal("radios")(err) {
		t.Errorf("err = %v", err)
	}
	if s := f.sets(); len(s) != 0 {
		t.Errorf("staged %q", s)
	}
}

// Review of #58, round 15.
func TestAnotherInterfaceOnTheGuestBridge(t *testing.T) {
	other := "network.visitors=interface\nnetwork.visitors.device='br-guest'\n"
	if st := newGuestFixture(t, guestNetworkShow+other, guestFirewallShow, "").status(); st.Guest.State != core.GuestForeign {
		t.Errorf("before creation: %+v", st.Guest)
	}
	st := newGuestFixture(t, guestNetworkShow+ourGuestNetwork+other, ourGuestFirewall, ourIsolatedWireless).status()
	if st.Guest.Blocked != core.GuestBlockedWeakened || st.Guest.BlockedBy != "network.visitors" {
		t.Errorf("with a guest network: %+v", st.Guest)
	}
}

func TestGuestWithoutItsForwardingIsWeakened(t *testing.T) {
	nw := guestNetworkShow + ourGuestNetwork
	for fw, want := range map[string]string{
		withoutSection(ourGuestFirewall, "firewall.guest_wan"):                                                       "firewall.guest_wan",
		ourGuestFirewall + "firewall.guest_wan.enabled='0'\n":                                                        "firewall.guest_wan",
		strings.Replace(ourGuestFirewall, "firewall.guest_wan.dest='wan'", "firewall.guest_wan.dest='elsewhere'", 1): "firewall.guest_wan.dest",
	} {
		st := newGuestFixture(t, nw, fw, ourIsolatedWireless).status()
		if st.Guest.Blocked != core.GuestBlockedWeakened || st.Guest.BlockedBy != want {
			t.Errorf("want %s: %+v", want, st.Guest)
		}
	}
}

func TestNoIPv6ForGuestsWhenTheHomeTakesTheOnlyPrefix(t *testing.T) {
	noULA := parseUCISections(strings.NewReplacer(
		"network.globals.ula_prefix='fd12:3456:789a::/48'\n", "",
		"ip6assign='60'", "ip6assign='64'",
	).Replace(guestNetworkShow))
	ipv6, renumber := guestIPv6(noULA, []ubus.Address{{Address: "2001:db8::", Mask: 64}})
	if ipv6 || renumber {
		t.Errorf("a sole /64 the home uses: ipv6=%v renumber=%v, want false false", ipv6, renumber)
	}
}

// Review of #58, round 16: 6 GHz allows WPA3 only, which the panel does not
// offer (D-104). Every radio means every radio but that one; asking for it is
// refused.
func TestGuestNetworkLeavesSixGHzOut(t *testing.T) {
	six := "wireless.radio2=wifi-device\nwireless.radio2.band='6g'\nwireless.radio2.channel='auto'\n"
	f := newGuestFixture(t, guestNetworkShow, guestFirewallShow, six)
	if _, err := f.m.StageGuest(core.GuestConfig{SSID: "Guests", Password: "guest-secret-1"}); err != nil {
		t.Fatal(err)
	}
	for _, s := range f.sets() {
		if strings.HasPrefix(s, "wireless.guest_radio2") {
			t.Errorf("a 6 GHz access point was staged: %q", s)
		}
	}
	f = newGuestFixture(t, guestNetworkShow, guestFirewallShow, six)
	if _, err := f.m.StageGuest(core.GuestConfig{SSID: "Guests", Password: "guest-secret-1", Radios: []string{"radio2"}}); !isRefusal("radios")(err) {
		t.Errorf("asking for 6 GHz: err = %v", err)
	}
}

// Review of #58, round 17.
func TestGuestGroupLedByAHandMadeAccessPoint(t *testing.T) {
	// A hand-made point with the guests' name, password and network, listed
	// before the panel's: one group, and it is still the guest network.
	lead := "wireless.extra=wifi-iface\nwireless.extra.device='radio0'\nwireless.extra.mode='ap'\nwireless.extra.network='guest'\nwireless.extra.ssid='Guests'\nwireless.extra.encryption='sae-mixed'\nwireless.extra.key='guest-secret-1'\n"
	f := newGuestFixture(t, guestNetworkShow+ourGuestNetwork, ourGuestFirewall, "")
	f.r.out["uci -q show wireless"] = []byte(string(readFixture(t, "wireless-25.12.5.uci")) + lead + ourIsolatedWireless)
	st := f.status()
	var id string
	for _, n := range st.Networks {
		if n.Kind == core.WiFiKindGuest {
			id = n.ID
		}
	}
	if id == "" || st.Guest.Network != id {
		t.Errorf("guest network %q, guest.network %q: they must match", id, st.Guest.Network)
	}
}

func TestGuestBlockedByDestinationLimitedNAT(t *testing.T) {
	fw := guestFirewallShow + "firewall.@zone[1].masq_dest='203.0.113.0/24'\n"
	if st := newGuestFixture(t, guestNetworkShow, fw, "").status(); st.Guest.Blocked != core.GuestBlockedNoNAT {
		t.Errorf("guest = %+v", st.Guest)
	}
}

func TestGuestRefusedWithOnlySixGHz(t *testing.T) {
	f := newGuestFixture(t, guestNetworkShow, guestFirewallShow, "")
	show := strings.NewReplacer("band='2g'", "band='6g'", "band='5g'", "band='6g'").Replace(string(f.r.out["uci -q show wireless"]))
	f.r.out["uci -q show wireless"] = []byte(show)
	if _, err := f.m.StageGuest(core.GuestConfig{SSID: "Guests", Password: "guest-secret-1"}); !isRefusal("radios")(err) {
		t.Errorf("err = %v", err)
	}
	if s := f.sets(); len(s) != 0 {
		t.Errorf("staged %q", s)
	}
}

// Review of #58, round 18.
func TestGuestBlockedByAForwardingFromEveryZone(t *testing.T) {
	fw := guestFirewallShow + "firewall.anyfwd=forwarding\nfirewall.anyfwd.src='*'\nfirewall.anyfwd.dest='lan'\n"
	if st := newGuestFixture(t, guestNetworkShow, fw, "").status(); st.Guest.Blocked != core.GuestBlockedOpenRule || st.Guest.BlockedBy != "anyfwd" {
		t.Errorf("guest = %+v", st.Guest)
	}
	// Even to the uplink (round 19): it runs before the guests' own rules
	// that keep them off private addresses upstream.
	up := guestFirewallShow + "firewall.anyfwd=forwarding\nfirewall.anyfwd.src='*'\nfirewall.anyfwd.dest='wan'\n"
	if st := newGuestFixture(t, guestNetworkShow, up, "").status(); st.Guest.Blocked != core.GuestBlockedOpenRule {
		t.Errorf("to the uplink: %+v", st.Guest)
	}
}

func TestAPortForwardReflectingIntoTheGuestZone(t *testing.T) {
	refl := "firewall.nas=redirect\nfirewall.nas.name='NAS'\nfirewall.nas.src='wan'\nfirewall.nas.dest='lan'\nfirewall.nas.reflection_zone='lan' 'guest'\n"
	if st := newGuestFixture(t, guestNetworkShow, guestFirewallShow+refl, "").status(); st.Guest.Blocked != core.GuestBlockedGuestRule || st.Guest.BlockedBy != "NAS" {
		t.Errorf("before creation: %+v", st.Guest)
	}
	st := newGuestFixture(t, guestNetworkShow+ourGuestNetwork, ourGuestFirewall+refl, ourIsolatedWireless).status()
	if st.Guest.Blocked != core.GuestBlockedWeakened || st.Guest.BlockedBy != "firewall.NAS" {
		t.Errorf("with a guest network: %+v", st.Guest)
	}
}

func TestZonesBoundBySubnet(t *testing.T) {
	// Chosen clear of a zone bound to 192.168.4.0/24 (192.168.3.0/24 is the uplink's).
	fw := guestFirewallShow + "firewall.@zone[2]=zone\nfirewall.@zone[2].name='iot'\nfirewall.@zone[2].subnet='192.168.4.0/24'\n"
	f := newGuestFixture(t, guestNetworkShow, fw, "")
	if _, err := f.m.StageGuest(core.GuestConfig{SSID: "Guests", Password: "guest-secret-1"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.sets(), "network.guest.ipaddr=192.168.5.1") {
		t.Errorf("subnet not clear of the zone: %q", f.sets())
	}
	// A guest network whose subnet a zone now covers.
	nw := guestNetworkShow + ourGuestNetwork + "network.guest.ipaddr='192.168.4.1'\n"
	cover := ourGuestFirewall + "firewall.@zone[2]=zone\nfirewall.@zone[2].name='iot'\nfirewall.@zone[2].subnet='192.168.0.0/16'\n"
	if st := newGuestFixture(t, nw, cover, ourIsolatedWireless).status(); st.Guest.BlockedBy != "firewall.iot.subnet" {
		t.Errorf("covered: %+v", st.Guest)
	}
}

// Review of #58, round 19: subnet selectors the guest subnet cannot be
// chosen clear of are refused.
func TestZoneSubnetSelectorsRefused(t *testing.T) {
	for _, sel := range []string{"!10.0.0.0/8", "fc00::/7", "0.0.0.0/1"} {
		fw := guestFirewallShow + "firewall.@zone[2]=zone\nfirewall.@zone[2].name='odd'\nfirewall.@zone[2].subnet='" + sel + "'\n"
		if st := newGuestFixture(t, guestNetworkShow, fw, "").status(); st.Guest.Blocked != core.GuestBlockedGuestRule || st.Guest.BlockedBy != "odd" {
			t.Errorf("%s before creation: %+v", sel, st.Guest)
		}
		ours := ourGuestFirewall + "firewall.@zone[2]=zone\nfirewall.@zone[2].name='odd'\nfirewall.@zone[2].subnet='" + sel + "'\n"
		if st := newGuestFixture(t, guestNetworkShow+ourGuestNetwork, ours, ourIsolatedWireless).status(); st.Guest.BlockedBy != "firewall.odd.subnet" {
			t.Errorf("%s with a guest network: %+v", sel, st.Guest)
		}
	}
}

// Review of #58, round 21: on in the settings, silent on the air when every
// radio it sits on is off.
func TestGuestOnAirFollowsItsRadios(t *testing.T) {
	nw := guestNetworkShow + ourGuestNetwork
	// Running: hostapd has the guest access point on radio0 up.
	running := `{"radio0":{"interfaces":[{"section":"default_radio0","ifname":"phy0-ap0"},{"section":"guest_radio0","ifname":"phy0-ap1"}]},
		"radio1":{"interfaces":[{"section":"default_radio1","ifname":"phy1-ap0"}]}}`
	f := newGuestFixture(t, nw, ourGuestFirewall, ourIsolatedWireless)
	f.r.out["/bin/ubus call network.wireless status"] = []byte(running)
	f.r.out["/bin/ubus call hostapd.phy0-ap1 get_status"] = []byte(`{"status":"ENABLED","channel":1,"freq":2412}`)
	f.r.out["/bin/ubus call hostapd.phy0-ap1 get_clients"] = []byte(clients24Empty)
	if st := f.status(); !st.Guest.OnAir {
		t.Errorf("a guest access point up: %+v", st.Guest)
	}
	// Staged but not applied (no guest access point running), or still
	// listening for radar: on in the settings, not on the air.
	if st := newGuestFixture(t, nw, ourGuestFirewall, ourIsolatedWireless).status(); st.Guest.State != core.GuestOn || st.Guest.OnAir {
		t.Errorf("nothing running: %+v", st.Guest)
	}
	f = newGuestFixture(t, nw, ourGuestFirewall, ourIsolatedWireless)
	f.r.out["/bin/ubus call network.wireless status"] = []byte(running)
	f.r.out["/bin/ubus call hostapd.phy0-ap1 get_status"] = []byte(`{"status":"DFS","channel":52,"freq":5260}`)
	f.r.out["/bin/ubus call hostapd.phy0-ap1 get_clients"] = []byte(clients24Empty)
	if st := f.status(); st.Guest.OnAir {
		t.Errorf("checking for radar: %+v", st.Guest)
	}
}

// Review of #58, round 22.
func TestAPortForwardIntoTheGuestZone(t *testing.T) {
	fwd := "firewall.cam=redirect\nfirewall.cam.name='Camera'\nfirewall.cam.src='wan'\nfirewall.cam.dest='guest'\n"
	st := newGuestFixture(t, guestNetworkShow+ourGuestNetwork, ourGuestFirewall+fwd, ourIsolatedWireless).status()
	if st.Guest.Blocked != core.GuestBlockedWeakened || st.Guest.BlockedBy != "firewall.Camera" {
		t.Errorf("guest = %+v", st.Guest)
	}
}

func TestGuestSubnetClearOfAnInterfaceThatIsDown(t *testing.T) {
	// iot is configured on 192.168.4.0/24 but down: not in the dump.
	nw := guestNetworkShow + "network.iot=interface\nnetwork.iot.proto='static'\nnetwork.iot.ipaddr='192.168.4.1'\nnetwork.iot.netmask='255.255.255.0'\n"
	f := newGuestFixture(t, nw, guestFirewallShow, "")
	if _, err := f.m.StageGuest(core.GuestConfig{SSID: "Guests", Password: "guest-secret-1"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.sets(), "network.guest.ipaddr=192.168.5.1") {
		t.Errorf("guest subnet: %q", f.sets())
	}
}

// Removed in the draft, still running until applied: on the air, not "none".
func TestGuestRemovalPendingIsStillOnTheAir(t *testing.T) {
	f := newGuestFixture(t, guestNetworkShow, guestFirewallShow, "")
	f.r.out["/bin/ubus call network.wireless status"] = []byte(`{"radio0":{"interfaces":[{"section":"default_radio0","ifname":"phy0-ap0"},{"section":"guest_radio0","ifname":"phy0-ap1"}]},"radio1":{"interfaces":[{"section":"default_radio1","ifname":"phy1-ap0"}]}}`)
	f.r.out["/bin/ubus call hostapd.phy0-ap1 get_status"] = []byte(`{"status":"ENABLED","channel":1,"freq":2412}`)
	f.r.out["/bin/ubus call hostapd.phy0-ap1 get_clients"] = []byte(clients24Empty)
	if st := f.status(); st.Guest.State != core.GuestNone || !st.Guest.OnAir {
		t.Errorf("guest = %+v, want none in the draft and still on the air", st.Guest)
	}
}

// Review of #58, final round.
func TestAKnownUplinkInNoZoneIsNoUplink(t *testing.T) {
	// The uplink is wwan (default route); the stock wan zone holds only wan.
	f := newGuestFixture(t, guestNetworkShow, guestFirewallShow, "")
	f.r.out["/bin/ubus call network.interface dump"] = []byte(`{"interface":[{"interface":"lan","up":true,"ipv4-address":[{"address":"192.168.1.1","mask":24}]},
		{"interface":"wwan","up":true,"ipv4-address":[{"address":"192.0.2.20","mask":24}],"route":[{"target":"0.0.0.0","mask":0,"nexthop":"192.0.2.1"}]}]}`)
	if st := f.status(); st.Guest.Blocked != core.GuestBlockedNoUplink {
		t.Errorf("guest = %+v", st.Guest)
	}
}

func TestRemovingAGuestNetworkWithNoAccessPointIsOneRow(t *testing.T) {
	// Every access point removed in LuCI; the rest is still the panel's.
	nw := guestNetworkShow + ourGuestNetwork
	staged := map[string]string{"network": guestNetworkShow, "wireless": string(readFixture(t, "wireless-25.12.5.uci")), "firewall": guestFirewallShow}
	committed := map[string]string{"network": nw, "wireless": string(readFixture(t, "wireless-25.12.5.uci")), "firewall": ourGuestFirewall}
	rows := stagedRows(t, committed, staged, "-network.guest", "-network.guest_dev", "-firewall.guest", "-firewall.guest_wan")
	if len(rows) != 1 || rows[0].LabelKey != "wireless.guest.section" || rows[0].From != "off||" || rows[0].To != "" {
		t.Errorf("rows = %+v, want one guest row", rows)
	}
}

// Review of #58, round 25.
func TestLANLeasesAreTheLocalNetworksOwn(t *testing.T) {
	all := []core.AddressLease{{IP: "192.168.1.120", MAC: "02:00:5e:00:53:01"}, {IP: "192.168.3.150", MAC: "02:00:5e:00:53:02"}}
	got := lanLeases(all, []string{"192.168.1.1/24"})
	if len(got) != 1 || got[0].IP != "192.168.1.120" {
		t.Errorf("leases = %+v, want the home one only", got)
	}
	if got := lanLeases(all, nil); len(got) != 2 {
		t.Errorf("no address known: %+v, want all", got)
	}
}

// What -restore-network leaves of a guest network (it does not touch Wi-Fi):
// the panel's access points on a network that is gone. Removable, not foreign.
func TestGuestLeftoversAfterAnEmergencyRestore(t *testing.T) {
	f := newGuestFixture(t, guestNetworkShow, guestFirewallShow, ourIsolatedWireless)
	st := f.status()
	if st.Guest.State == core.GuestForeign || st.Guest.State == core.GuestNone || st.Guest.Blocked != core.GuestBlockedWeakened {
		t.Errorf("guest = %+v, want the panel's, weakened", st.Guest)
	}
	f = newGuestFixture(t, guestNetworkShow, guestFirewallShow, ourIsolatedWireless)
	if _, err := f.m.StageGuestRemove(); err != nil {
		t.Fatal(err)
	}
	if got := f.sets(); !slices.Equal(got, []string{"-wireless.guest_radio0", "-wireless.guest_radio1"}) {
		t.Errorf("remove: %q", got)
	}
}

func TestGuestWithNoAccessPointLeftIsOffAndRemovable(t *testing.T) {
	f := newGuestFixture(t, guestNetworkShow+ourGuestNetwork, ourGuestFirewall, "")
	if st := f.status(); st.Guest.State != core.GuestOff {
		t.Errorf("guest = %+v, want off", st.Guest)
	}
	f = newGuestFixture(t, guestNetworkShow+ourGuestNetwork, ourGuestFirewall, "")
	if _, err := f.m.StageGuestRemove(); err != nil {
		t.Errorf("remove: %v", err)
	}
}

// A hole that is live stays on the card until its fix is applied.
func TestGuestWarningFollowsTheLiveFirewall(t *testing.T) {
	nw := guestNetworkShow + ourGuestNetwork
	f := newGuestFixture(t, nw, ourGuestFirewall, ourIsolatedWireless)
	live := map[string]string{
		"network":  nw,
		"firewall": ourGuestFirewall + "firewall.panel=rule\nfirewall.panel.name='Guests to the panel'\nfirewall.panel.src='guest'\nfirewall.panel.target='ACCEPT'\n",
		"wireless": string(f.r.out["uci -q show wireless"]),
		"dhcp":     "",
	}
	inner := f.m.net.run
	f.m.net.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "uci" && len(args) == 5 && args[1] == "-c" && args[3] == "show" {
			cfg := strings.TrimPrefix(args[4], committedPrefix)
			return []byte(strings.ReplaceAll(live[cfg], cfg+".", args[4]+".")), nil
		}
		return inner(ctx, name, args...)
	}
	st := f.status()
	if st.Guest.Blocked != core.GuestBlockedWeakened || st.Guest.BlockedBy != "firewall.Guests to the panel" {
		t.Errorf("draft fixed, router not yet: %+v", st.Guest)
	}
}

// Review of #58, round 26.
func TestGuestIsolationUncheckedWithoutTheLiveFiles(t *testing.T) {
	f := newGuestFixture(t, guestNetworkShow+ourGuestNetwork, ourGuestFirewall, ourIsolatedWireless)
	inner := f.m.net.run
	f.m.net.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "uci" && len(args) > 1 && args[1] == "-c" {
			return nil, errors.New("no space left on device")
		}
		return inner(ctx, name, args...)
	}
	if st := f.status(); st.Guest.Blocked != core.GuestBlockedUnchecked {
		t.Errorf("guest = %+v, want unchecked", st.Guest)
	}
}

func TestGuestIPv6CountsOtherInterfacesPrefixes(t *testing.T) {
	// No ULA; a delegated /63: lan /64 and iot /64 take it all.
	nw := parseUCISections(strings.NewReplacer(
		"network.globals.ula_prefix='fd12:3456:789a::/48'\n", "",
		"ip6assign='60'", "ip6assign='64'",
	).Replace(guestNetworkShow) + "network.iot=interface\nnetwork.iot.ip6assign='64'\n")
	if ipv6, _ := guestIPv6(nw, []ubus.Address{{Address: "2001:db8::", Mask: 63}}); ipv6 {
		t.Error("a /63 the home and iot already use: ipv6 = true")
	}
}

func TestANetworkIsOnWhenOneOfItsAccessPointsIs(t *testing.T) {
	if !anyEnabled([]uciSection{{options: map[string]string{"disabled": "1"}}, {options: map[string]string{}}}) {
		t.Error("one of two on: off")
	}
	if anyEnabled([]uciSection{{options: map[string]string{"disabled": "1"}}}) {
		t.Error("all off: on")
	}
}

// Review of #58, round 27.
func TestAZoneOnPartOfTheGuestSubnet(t *testing.T) {
	nw := guestNetworkShow + ourGuestNetwork + "network.guest.ipaddr='192.168.4.1'\nnetwork.guest.netmask='255.255.255.0'\n"
	fw := ourGuestFirewall + "firewall.@zone[2]=zone\nfirewall.@zone[2].name='half'\nfirewall.@zone[2].subnet='192.168.4.128/25'\n"
	if st := newGuestFixture(t, nw, fw, ourIsolatedWireless).status(); st.Guest.BlockedBy != "firewall.half.subnet" {
		t.Errorf("guest = %+v", st.Guest)
	}
}

func TestGuestBlockedByARestrictedDHCPServer(t *testing.T) {
	f := newGuestFixture(t, guestNetworkShow, guestFirewallShow, "")
	f.r.out["uci -q show dhcp"] = []byte("dhcp.@dnsmasq[0]=dnsmasq\ndhcp.@dnsmasq[0].interface='lan'\n")
	if st := f.status(); st.Guest.Blocked != core.GuestBlockedNoDHCP {
		t.Errorf("guest = %+v", st.Guest)
	}
}

func TestPickUplinkFollowsWANInfo(t *testing.T) {
	dr := []ubus.Route{{Target: "0.0.0.0", Mask: 0, Nexthop: "192.0.2.1"}}
	cases := []struct {
		ifaces []ubus.Interface
		want   string
	}{
		{[]ubus.Interface{{Name: "lan"}, {Name: "wwan", Route: dr}}, "wwan"},
		{[]ubus.Interface{{Name: "vpn", Route: dr}, {Name: "wan", Route: dr}}, "wan"},
		{[]ubus.Interface{{Name: "lan"}}, ""},
	}
	for _, tc := range cases {
		if got := pickUplink(tc.ifaces); got != tc.want {
			t.Errorf("pickUplink = %q, want %q", got, tc.want)
		}
	}
}

// Review of #58, round 28: dnsmasq exclusions, and an existing guest network.
func TestGuestBlockedByDHCPExclusionsBeforeAndAfter(t *testing.T) {
	for _, dh := range []string{
		"dhcp.@dnsmasq[0]=dnsmasq\ndhcp.@dnsmasq[0].notinterface='guest'\n",
		"dhcp.@dnsmasq[0]=dnsmasq\ndhcp.@dnsmasq[0].notinterface='br-guest'\n",
	} {
		f := newGuestFixture(t, guestNetworkShow, guestFirewallShow, "")
		f.r.out["uci -q show dhcp"] = []byte(dh)
		if st := f.status(); st.Guest.Blocked != core.GuestBlockedNoDHCP {
			t.Errorf("%q before creation: %+v", dh, st.Guest)
		}
	}
	f := newGuestFixture(t, guestNetworkShow+ourGuestNetwork, ourGuestFirewall, ourIsolatedWireless)
	f.r.out["uci -q show dhcp"] = []byte("dhcp.@dnsmasq[0]=dnsmasq\ndhcp.@dnsmasq[0].interface='lan'\ndhcp.guest=dhcp\n")
	if st := f.status(); st.Guest.Blocked != core.GuestBlockedNoDHCP {
		t.Errorf("with a guest network: %+v", st.Guest)
	}
}
