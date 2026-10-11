package openwrt

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/veilbridge-os/veilbridge/internal/adapters/openwrt/ubus"
	"github.com/veilbridge-os/veilbridge/internal/core"
)

// The guest network (#58). The recipe was measured on the reference router
// (Xiaomi AX3000T, 25.12.5) on 10.10.2026 with simulated clients in network
// namespaces, before any of this was written (D-105, D-106):
//
//   - one bridge for every radio. Without it the interface takes ONE access
//     point and the other comes up with no address: guests on 2.4 GHz got
//     nothing;
//   - `isolate` AND `bridge_isolate` on each access point. `isolate` only
//     separates the clients of one access point; the bridge port stayed
//     `isolated off` and a 2.4 GHz guest could reach a 5 GHz one;
//   - a zone that refuses everything to the router except DHCP, DNS and the
//     ICMPv6 that neighbour discovery needs. Without the ICMPv6 rule a guest
//     had a SLAAC address and no IPv6 at all, not even DNS;
//   - private destinations refused on the way out: a guest reached a
//     computer in the network ABOVE the router, which is "the internet" to
//     the firewall when the router sits behind another one (D-106);
//   - with the stock `lan.ip6assign=60` and a delegated /62 the guest network
//     got no IPv6 prefix at all, not even ULA; with 64 it got one from each.
//     So making a guest network may renumber the home network once (D-106).
//
// The panel's guest network is recognised by its own named sections. A guest
// network made by hand (LuCI) is left alone and shown as it is.

const (
	guestSection    = "guest"     // network.guest, dhcp.guest, firewall.guest
	guestDevSection = "guest_dev" // network.guest_dev: the bridge
	guestBridge     = "br-guest"
	guestPrefix     = "guest_" // wireless.guest_<radio>, firewall.guest_<rule>
	guestZone       = "guest"
	// guestAssign is the prefix length every network gets for IPv6 (D-106).
	guestAssign = 64
)

// netifdWirelessScript is where netifd declares the wifi-iface options it
// knows. `bridge_isolate` is in it on 25.12.5 and on 23.05.5 (read on the VM);
// a build without it cannot keep 2.4 GHz guests from 5 GHz ones.
const netifdWirelessScript = "/lib/netifd/netifd-wireless.sh"

// guestRules are the firewall sections a guest network adds besides its zone.
var guestRules = []string{"guest_wan", "guest_dhcp", "guest_dns", "guest_icmpv6", "guest_dhcpv6", "guest_no_private4", "guest_no_private6"}

// privateV4 and privateV6 are refused from guests to the uplink (D-106).
var (
	privateV4 = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "169.254.0.0/16"}
	privateV6 = []string{"fc00::/7"}
)

// guestRuleSpec is one firewall rule of the guest network, exactly as the
// panel writes it. The same table checks it later: a rule that no longer
// reads like this is a protection changed outside the panel (review of #58).
type guestRuleSpec struct {
	id, name string
	opts     []guestOpt
	lists    []guestList
}

type guestOpt struct{ key, value string }

type guestList struct {
	key    string
	values []string
}

func guestRuleSpecs(wan string) []guestRuleSpec {
	src := guestOpt{"src", guestZone}
	return []guestRuleSpec{
		{"guest_dhcp", "Guest DHCP", []guestOpt{src, {"proto", "udp"}, {"dest_port", "67-68"}, {"target", "ACCEPT"}}, nil},
		{"guest_dns", "Guest DNS", []guestOpt{src, {"proto", "tcp udp"}, {"dest_port", "53"}, {"target", "ACCEPT"}}, nil},
		{"guest_icmpv6", "Guest ICMPv6", []guestOpt{src, {"proto", "icmp"}, {"family", "ipv6"}, {"target", "ACCEPT"}},
			[]guestList{{"icmp_type", []string{"router-solicitation", "neighbour-solicitation", "neighbour-advertisement"}}}},
		{"guest_dhcpv6", "Guest DHCPv6", []guestOpt{src, {"proto", "udp"}, {"family", "ipv6"}, {"dest_port", "547"}, {"target", "ACCEPT"}}, nil},
		{"guest_no_private4", "Guest no private IPv4", []guestOpt{src, {"dest", wan}, {"family", "ipv4"}, {"proto", "all"}, {"target", "REJECT"}},
			[]guestList{{"dest_ip", privateV4}}},
		{"guest_no_private6", "Guest no private IPv6", []guestOpt{src, {"dest", wan}, {"family", "ipv6"}, {"proto", "all"}, {"target", "REJECT"}},
			[]guestList{{"dest_ip", privateV6}}},
	}
}

// matches says whether a rule section reads exactly as the spec: every
// option the panel wrote, list items in order, and nothing more - an extra
// option (src_ip, dest) narrows a REJECT or widens an ACCEPT.
func (r guestRuleSpec) matches(s uciSection) bool {
	if s.kind != "rule" {
		return false
	}
	want := map[string]string{}
	for _, o := range r.opts {
		want[o.key] = o.value
	}
	for _, l := range r.lists {
		want[l.key] = strings.Join(l.values, " ")
	}
	got := 0
	for k, v := range s.options {
		switch {
		case k == "name":
			continue
		case k == "enabled" && v == "1":
			continue
		default:
			// The key must be one the panel wrote: an empty option the
			// recipe does not have must not stand in for a missing one.
			if w, ok := want[k]; !ok || w != v {
				return false
			}
		}
		got++
	}
	return got == len(want)
}

// isGuestSection reports whether a section belongs to the panel's guest
// network by its name. That alone does not make it the panel's: see guestShape.
func isGuestSection(config, section string) bool {
	switch config {
	case "network", "wireless", "dhcp", "firewall":
		return section == guestSection || strings.HasPrefix(section, guestPrefix)
	}
	return false
}

// guestShape is the guest network as the configuration files hold it.
type guestShape struct {
	ours    bool
	foreign bool
	ifaces  []uciSection // wireless guest_<radio> on the guest network: its access points
	// owned is every wifi-iface under the panel's names, wherever it is
	// attached now: one moved to lan in LuCI is still the panel's, and must
	// show as a weakened protection and go when the guest network goes.
	owned []uciSection
	// stray: access points attached to the guest network under other names
	// (added in LuCI) - not isolated by the panel, and not turned off with it.
	stray []uciSection
}

// readGuestShape: network, firewall, dhcp and EVERY wireless section (not only
// access points) - a section of any kind under one of the panel's names makes
// the guest network one the panel did not make: writing over it would turn a
// hand-made access point into the panel's (review of #58).
func readGuestShape(network, firewall, wireless, dhcp []uciSection) guestShape {
	var g guestShape
	byID := map[string]uciSection{}
	for _, s := range network {
		byID[s.id] = s
	}
	dev, iface := byID[guestDevSection], byID[guestSection]
	g.ours = dev.kind == "device" && dev.options["name"] == guestBridge &&
		iface.kind == "interface" && iface.options["device"] == guestBridge
	if !g.ours && dev.kind == "" && iface.kind == "" {
		// What `-restore-network` leaves of a confirmed guest network: it puts
		// back network, address handout and firewall but not Wi-Fi (the
		// main network's password policy), so only the panel's access points
		// remain, attached to a network that is gone. They are the panel's to
		// remove, not a hand-made guest network (review of #58).
		orphans := 0
		clean := true
		for _, s := range wireless {
			if strings.HasPrefix(s.id, guestPrefix) {
				if s.kind == "wifi-iface" && s.options["network"] == guestSection {
					orphans++
				} else {
					clean = false
				}
			}
		}
		for _, s := range firewall {
			if s.id == guestSection || contains(guestRules, s.id) || (s.kind == "zone" && s.options["name"] == guestZone) {
				clean = false
			}
		}
		for _, s := range dhcp {
			if s.id == guestSection {
				clean = false
			}
		}
		g.ours = orphans > 0 && clean
	}
	if g.ours {
		for _, s := range wireless {
			if s.kind != "wifi-iface" {
				continue
			}
			if !strings.HasPrefix(s.id, guestPrefix) {
				if onGuest(s) {
					g.stray = append(g.stray, s)
				}
				continue
			}
			g.owned = append(g.owned, s)
			if s.options["network"] == guestSection {
				g.ifaces = append(g.ifaces, s)
			}
		}
		return g
	}
	if iface.kind != "" || dev.kind != "" {
		g.foreign = true
	}
	for _, s := range firewall {
		if s.id == guestSection || contains(guestRules, s.id) {
			g.foreign = true
		}
	}
	for _, s := range dhcp {
		if s.id == guestSection {
			g.foreign = true
		}
	}
	for _, s := range wireless {
		// Under the panel's name, or already attached to a "guest" network
		// (an access point left by an earlier one): made again, it would join
		// the bridge without the isolation the panel sets (review of #58).
		if strings.HasPrefix(s.id, guestPrefix) || (s.kind == "wifi-iface" && onGuest(s)) {
			g.foreign = true
		}
	}
	for _, s := range network {
		if s.kind == "device" && s.options["name"] == guestBridge {
			g.foreign = true
		}
		if otherOnGuestBridge(s) {
			g.foreign = true
		}
	}
	for _, s := range firewall {
		if s.kind == "zone" && s.options["name"] == guestZone {
			g.foreign = true
		}
	}
	return g
}

// otherOnGuestBridge: an interface other than the guests' own sits on the
// guests' bridge. Its zone - lan, say - would then hold the bridge too, and
// its policies would apply to guests (review of #58).
func otherOnGuestBridge(s uciSection) bool {
	if s.kind != "interface" || s.id == guestSection {
		return false
	}
	return s.options["device"] == guestBridge || contains(strings.Fields(s.options["ifname"]), guestBridge)
}

// onGuest: the access point is attached to the guest network - `network` is
// a list, and `'guest lan'` attaches it to both (review of #58).
func onGuest(s uciSection) bool {
	return contains(strings.Fields(s.options["network"]), guestSection)
}

// enabled: at least one of its access points is on.
func (g guestShape) enabled() bool {
	for _, s := range g.ifaces {
		if s.options["disabled"] != "1" {
			return true
		}
	}
	return false
}

// guestWords is the guest network in the one value the apply bar shows for
// it: "" when there is none, else "on|2.4,5|Guests" — state, bands, name.
// The panel words it (ApplyBar); the name goes last because it may hold "|".
func guestWords(g guestShape, radios []uciSection) string {
	if !g.ours {
		return ""
	}
	if len(g.ifaces) == 0 {
		// The panel's own with no access point on it (all removed or moved
		// in LuCI): still a guest network, off - its removal must still read
		// as one, or the draft guard would not see it (review of #58).
		name := ""
		if len(g.owned) > 0 {
			name = g.owned[0].options["ssid"]
		}
		return "off||" + name
	}
	band := map[string]string{}
	for _, r := range radios {
		band[r.id] = bandWord(r.options["band"])
	}
	var bands []string
	for _, s := range g.ifaces {
		if b := band[s.options["device"]]; b != "" && !contains(bands, b) {
			bands = append(bands, b)
		}
	}
	sort.Slice(bands, func(i, j int) bool {
		a, _ := strconv.ParseFloat(bands[i], 64)
		b, _ := strconv.ParseFloat(bands[j], 64)
		return a < b
	})
	state := "off"
	if g.enabled() {
		state = "on"
	}
	return state + "|" + strings.Join(bands, ",") + "|" + g.ifaces[0].options["ssid"]
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// guestRow is the ONE row of the apply bar for the whole guest network,
// however many sections the router needs for it.
func guestRow(from, to string) core.ConfigChange {
	said := describe("wireless", roleGuest, "")
	return core.ConfigChange{
		Label: said.words, LabelKey: said.key, From: from, To: to,
		Dangerous: true, Detail: "network." + guestSection,
	}
}

// lanAssignRow is the row that says the home network's IPv6 addresses change.
func lanAssignRow(from, to string) core.ConfigChange {
	said := describe("network", roleLAN, "ip6assign")
	return core.ConfigChange{
		Label: said.words, LabelKey: said.key, From: from, To: to,
		Dangerous: dangerousConfig("network"), Detail: "network." + lanSection + ".ip6assign",
	}
}

// guestIPv6 says whether guests get IPv6 and whether the home network has to
// be renumbered for it (D-106). The home network takes 2^(64-assign) /64s of
// every prefix; there is room only when a delegated prefix is larger than
// that. Without a delegated prefix the measured answer (nothing, not even
// ULA, with assign 60) is taken as the rule: renumber.
func guestIPv6(network []uciSection, prefixes []ubus.Address) (ipv6, renumber bool) {
	ula := ""
	assign := 0
	for _, s := range network {
		if s.kind == "globals" {
			ula = s.options["ula_prefix"]
		}
		if s.id == lanSection {
			assign, _ = strconv.Atoi(s.options["ip6assign"])
		}
	}
	// How many /64s a prefix holds, and how many the home network takes.
	capacity := func(mask int) int {
		if mask <= 0 || mask > guestAssign {
			return 0
		}
		if mask < guestAssign-16 {
			mask = guestAssign - 16 // enough to compare; avoids overflow on a /48
		}
		return 1 << (guestAssign - mask)
	}
	home := 0
	if assign > 0 && assign <= guestAssign {
		home = capacity(assign)
	}
	// Other interfaces that take /64s from the same prefixes (an IoT network,
	// say) leave that much less for guests (review of #58).
	others := 0
	for _, s := range network {
		if s.kind != "interface" || s.id == lanSection || s.id == guestSection {
			continue
		}
		if a, err := strconv.Atoi(s.options["ip6assign"]); err == nil && a > 0 && a <= guestAssign {
			others += capacity(a)
		}
	}
	if assign > 0 && assign < guestAssign {
		renumber = true
		for _, p := range prefixes {
			if capacity(p.Mask) > home+others {
				renumber = false
			}
		}
	}
	if renumber {
		home = 1
	}
	home += others
	// Guests get IPv6 when some prefix has a /64 left over after the home
	// network - a sole delegated /64 the home already uses does not count
	// (review of #58).
	all := append([]ubus.Address{}, prefixes...)
	if _, n, err := net.ParseCIDR(ula); err == nil {
		ones, _ := n.Mask.Size()
		all = append(all, ubus.Address{Address: n.IP.String(), Mask: ones})
	}
	for _, p := range all {
		if capacity(p.Mask) > home {
			return true, renumber
		}
	}
	return false, false
}

// delegatedPrefixes are the IPv6 prefixes the uplink was handed.
func delegatedPrefixes(ifaces []ubus.Interface) []ubus.Address {
	var out []ubus.Address
	for _, i := range ifaces {
		out = append(out, i.IPv6Prefix...)
	}
	return out
}

// minLocalBits: a route wider than the widest private range (10/8) is a way
// to the internet - a default route, or a full-tunnel VPN's 0.0.0.0/1 and
// 128.0.0.0/1 - and occupies no local network (review of #58).
const minLocalBits = 8

// freeGuestSubnet picks the first /24 that overlaps nothing the router has:
// no address of any interface and no route. The network above the router is
// private too (a home router in front of this one), so a fixed 192.168.3.0
// could collide with it.
func freeGuestSubnet(ifaces []ubus.Interface, kernel []netip.Prefix) (string, error) {
	var taken []*net.IPNet
	// Routes the kernel has that netifd does not report (a VPN's, added by
	// another program) count too (review of #58).
	for _, p := range kernel {
		if p.Addr().Is4() && p.Bits() >= minLocalBits {
			add := p.Masked()
			taken = append(taken, &net.IPNet{IP: add.Addr().AsSlice(), Mask: net.CIDRMask(add.Bits(), 32)})
		}
	}
	add := func(addr string, mask int) {
		if _, n, err := net.ParseCIDR(addr + "/" + strconv.Itoa(mask)); err == nil && n.IP.To4() != nil {
			taken = append(taken, n)
		}
	}
	for _, i := range ifaces {
		for _, a := range i.IPv4Address {
			add(a.Address, a.Mask)
		}
		for _, r := range i.Route {
			if r.Mask >= minLocalBits { // a default route (or a VPN's 0/1 + 128/1) is the internet, not a network here
				add(r.Target, r.Mask)
			}
		}
	}
	free := func(cidr string) bool {
		_, c, _ := net.ParseCIDR(cidr)
		for _, t := range taken {
			if t.Contains(c.IP) || c.Contains(t.IP) {
				return false
			}
		}
		return true
	}
	for _, base := range []string{"192.168.%d.0/24", "10.%d.0.0/24"} {
		for i := 3; i < 255; i++ {
			if cidr := fmt.Sprintf(base, i); free(cidr) {
				return cidr, nil
			}
		}
	}
	return "", errors.New("openwrt: no free private network for guests")
}

// guestState is everything a guest edit or the status needs, read at once.
type guestState struct {
	cfg      wifiConfig
	network  []uciSection
	firewall []uciSection
	dhcp     []uciSection
	shape    guestShape
	ifaces   []ubus.Interface
	// ifacesErr: the dump failed. The status lives without it; making a guest
	// network does not, since its subnet is chosen against these.
	ifacesErr error
}

func (m *wifiManager) readGuestState(ctx context.Context) (guestState, error) {
	cfg, err := m.readConfig(ctx)
	if err != nil {
		return guestState{}, err
	}
	return m.guestStateFor(ctx, cfg)
}

// guestStateFor is readGuestState for a caller that has read wireless already.
func (m *wifiManager) guestStateFor(ctx context.Context, cfg wifiConfig) (guestState, error) {
	st := guestState{cfg: cfg}
	nw, err := m.net.run(ctx, "uci", "-q", "show", "network")
	if err != nil {
		return st, fmt.Errorf("openwrt: read network: %w", err)
	}
	fw, err := m.net.run(ctx, "uci", "-q", "show", "firewall")
	if err != nil {
		return st, fmt.Errorf("openwrt: read firewall: %w", err)
	}
	st.network, st.firewall = parseUCISections(string(nw)), parseUCISections(string(fw))
	// An unreadable dhcp is not an empty one: a hand-made dhcp.guest would
	// pass the name check and be written over.
	dh, err := m.net.run(ctx, "uci", "-q", "show", "dhcp")
	if err != nil {
		return st, fmt.Errorf("openwrt: read dhcp: %w", err)
	}
	st.dhcp = parseUCISections(string(dh))
	st.shape = readGuestShape(st.network, st.firewall, cfg.all, st.dhcp)
	if m.bus == nil {
		st.ifacesErr = core.ErrNotImplemented
	} else {
		st.ifaces, st.ifacesErr = m.bus.Interfaces(ctx)
	}
	return st, nil
}

// guestStateCommitted is guestStateFor over the files as they are on disk,
// without the draft: what the router does now. A hole in the guests'
// isolation that is live stays on the card until its fix is applied, not
// only until the fix is staged (review of #58).
func (m *wifiManager) guestStateCommitted(ctx context.Context, ifaces []ubus.Interface) (guestState, error) {
	read := func(c string) ([]uciSection, error) {
		var out []byte
		err := m.net.withCommitted(c, func(dir, alias string) error {
			var e error
			out, e = m.net.run(ctx, "uci", "-q", "-c", dir, "show", alias)
			return e
		})
		return parseUCISections(string(out)), err
	}
	st := guestState{ifaces: ifaces}
	all, err := read("wireless")
	if err != nil {
		return st, err
	}
	st.cfg.all = all
	for _, s := range all {
		switch {
		case s.kind == "wifi-device":
			st.cfg.radios = append(st.cfg.radios, s)
		case s.kind == "wifi-iface" && s.options["mode"] == "ap":
			st.cfg.ifaces = append(st.cfg.ifaces, s)
		}
	}
	for c, dst := range map[string]*[]uciSection{"network": &st.network, "firewall": &st.firewall, "dhcp": &st.dhcp} {
		if *dst, err = read(c); err != nil {
			return st, err
		}
	}
	if len(st.firewall) == 0 {
		return st, errors.New("openwrt: no committed firewall configuration read")
	}
	st.shape = readGuestShape(st.network, st.firewall, st.cfg.all, st.dhcp)
	return st, nil
}

// guestSupported: the device has a radio and netifd can isolate bridge ports.
func (m *wifiManager) guestSupported(cfg wifiConfig) bool {
	if len(cfg.radios) == 0 {
		return false
	}
	read := m.readFile
	if read == nil {
		read = os.ReadFile
	}
	b, err := read(netifdWirelessScript)
	return err == nil && strings.Contains(string(b), "bridge_isolate")
}

// guestBlocker says why guests could not be kept from the home network here,
// and by what (a zone or a rule name); "" when nothing stands in the way.
//
//   - the uplink in the same zone as the home network: forwarding guests to
//     the uplink forwards them home too (one of our own stands is built so);
//   - an enabled rule accepting traffic from every zone (`src '*'`): measured
//     on the reference router, fw4 places it in `input`/`forward` BEFORE the
//     per-zone jumps, so the guests' zone never sees that traffic.
func (m *wifiManager) guestBlocker(st guestState) (string, string) {
	zone, nets := m.uplinkZone(st.firewall, st.ifaces)
	if zone == "" {
		return core.GuestBlockedNoUplink, ""
	}
	if contains(nets, lanSection) || zoneHoldsHome(st.firewall, st.network, zone) {
		return core.GuestBlockedSharedZone, zone
	}
	// Guests leave through the uplink's NAT; a zone that does not masquerade
	// every source (masq off, or masq_src naming the home subnet) would send
	// the guests' private addresses out with no way back (review of #58).
	for _, s := range st.firewall {
		if s.kind == "zone" && s.options["name"] == zone && (s.options["masq"] != "1" || s.options["masq_src"] != "" || s.options["masq_dest"] != "") {
			return core.GuestBlockedNoNAT, zone
		}
	}
	// IPv6 leaves through its own uplink interface; in another zone than
	// IPv4's, the guests' forwarding would not cover it.
	if z6 := ipv6UplinkZone(st); z6 != "" && z6 != zone {
		return core.GuestBlockedSplitUplink, z6
	}
	// From every zone: fw4 puts these before the per-zone jumps, so the
	// guests' zone never sees that traffic - an accepting rule, a forwarding
	// anywhere but the uplink, a port forward (review of #58).
	for _, s := range st.firewall {
		if s.options["src"] != "*" || s.options["enabled"] == "0" {
			continue
		}
		t := s.options["target"]
		// A forwarding from every zone even to the uplink: it runs before the
		// guests' own rules that keep them off private addresses upstream.
		if (s.kind == "rule" && (t == "" || strings.EqualFold(t, "ACCEPT"))) ||
			s.kind == "forwarding" || s.kind == "redirect" {
			return core.GuestBlockedOpenRule, firstNonEmpty(s.options["name"], s.id)
		}
	}
	// dnsmasq limited to named interfaces, or told to skip the guests',
	// would not serve them: no address, no DNS - before a guest network is
	// made and after (review of #58). Said, not worked around.
	if list := dhcpSkipsGuests(st.dhcp); list != "" {
		return core.GuestBlockedNoDHCP, list
	}
	if st.shape.ours {
		if what := m.guestWeakened(st); what != "" {
			return core.GuestBlockedWeakened, what
		}
		return "", ""
	}
	// Before there is a guest network: a section left from an earlier one (or
	// written by hand) that lets the guests' zone in would come alive with the
	// new zone, and a zone already binding the bridge would take the guests
	// (review of #58).
	if name := guestOpenings(st, zone, nil); name != "" {
		return core.GuestBlockedGuestRule, name
	}
	if name := zoneTakingGuests(st.firewall); name != "" {
		return core.GuestBlockedGuestRule, name
	}
	if name := subnetHazard(st.firewall); name != "" {
		return core.GuestBlockedGuestRule, name
	}

	return "", ""
}

// zoneTakingGuests names a zone other than the guests' own that holds the
// guest interface (`network`) or its bridge (`device`, wildcards included:
// fw4 matches `br-*` and `br+` the way nftables does).
func zoneTakingGuests(firewall []uciSection) string {
	for _, s := range firewall {
		if s.kind != "zone" || s.id == guestSection {
			continue
		}
		if contains(strings.Fields(s.options["network"]), guestSection) {
			return firstNonEmpty(s.options["name"], s.id)
		}
		if zoneBindsDevice(s, guestBridge) {
			return firstNonEmpty(s.options["name"], s.id)
		}
	}
	return ""
}

// ipv6UplinkZone is the zone of the IPv6 uplink: the interface with an IPv6
// default route, else one configured as a DHCPv6 client (wan6); "" when there
// is none or it is in no zone.
func ipv6UplinkZone(st guestState) string {
	name := ""
	for _, i := range st.ifaces {
		if _, ok := i.DefaultRoute6(); ok {
			name = i.Name
			break
		}
	}
	if name == "" {
		for _, s := range st.network {
			if s.kind == "interface" && s.options["proto"] == "dhcpv6" {
				name = s.id
				break
			}
		}
	}
	if name == "" {
		return ""
	}
	for _, s := range st.firewall {
		if s.kind == "zone" && contains(strings.Fields(s.options["network"]), name) {
			return s.options["name"]
		}
	}
	return ""
}

// guestOpt4 reads an option of the guest interface from `uci show network`.
func guestOpt4(network []uciSection, key string) string {
	for _, s := range network {
		if s.id == guestSection {
			return s.options[key]
		}
	}
	return ""
}

// zoneBindsDevice: one of the zone's `device` entries names dev, as such or
// by a wildcard (fw4 matches `br-*` and `br+` the way nftables does). A
// negation still names it: strict on purpose.
func zoneBindsDevice(zone uciSection, dev string) bool {
	for _, d := range strings.Fields(zone.options["device"]) {
		d = strings.TrimPrefix(d, "!")
		pat := strings.TrimSuffix(strings.TrimSuffix(d, "*"), "+")
		if d == dev || (pat != d && strings.HasPrefix(dev, pat)) {
			return true
		}
	}
	return false
}

// zoneHoldsHome: the zone holds the home network - by its interface, or by
// its bridge device (review of #58: `device='br-lan'` on the uplink's zone
// forwarded guests home as surely as `network='lan'`).
func zoneHoldsHome(firewall, network []uciSection, zoneName string) bool {
	lanDev := "br-lan"
	for _, s := range network {
		if s.id == lanSection && s.options["device"] != "" {
			lanDev = s.options["device"]
		}
	}
	for _, s := range firewall {
		if s.kind != "zone" || s.options["name"] != zoneName {
			continue
		}
		if contains(strings.Fields(s.options["network"]), lanSection) || zoneBindsDevice(s, lanDev) {
			return true
		}
	}
	return false
}

// guestOpenings names the first enabled section that lets the guests' zone
// somewhere it must not go: an accepting rule (to the router - the panel - or
// to the home network), a port forward, or a forwarding to any zone but the
// uplink's. The panel's own sections (skip) are checked against their recipe
// separately.
func guestOpenings(st guestState, wan string, skip map[string]bool) string {
	for _, s := range st.firewall {
		if skip[s.id] || s.options["src"] != guestZone || s.options["enabled"] == "0" {
			continue
		}
		t := s.options["target"]
		switch {
		case s.kind == "rule" && (t == "" || strings.EqualFold(t, "ACCEPT")),
			s.kind == "redirect",
			s.kind == "forwarding" && s.options["dest"] != wan:
			return firstNonEmpty(s.options["name"], s.id)
		}
	}
	// A port forward from elsewhere that reflects into the guests' zone: fw4
	// then lets guests reach the forwarded home service by the router's
	// outside address (review of #58).
	// A port forward INTO the guests' zone too: fw4 reflects it into its
	// destination zone by default, and a guest reaches another guest by the
	// router's outside address, past the access points' isolation.
	for _, s := range st.firewall {
		if s.kind != "redirect" || s.options["enabled"] == "0" {
			continue
		}
		reflects := s.options["reflection"] != "0" && contains(strings.Fields(s.options["reflection_zone"]), guestZone)
		if reflects || s.options["dest"] == guestZone {
			return firstNonEmpty(s.options["name"], s.id)
		}
	}
	return ""
}

// subnetHazard names a zone bound by a `subnet` selector the guest subnet
// cannot be chosen clear of: a negation (`!10.0.0.0/8` matches everything
// else), an IPv6 range (guests' addresses come from prefixes the panel does
// not pick), or an IPv4 range wider than the widest private one. Refused
// rather than reasoned about (review of #58).
func subnetHazard(firewall []uciSection) string {
	for _, s := range firewall {
		if s.kind != "zone" || s.id == guestSection {
			continue
		}
		for _, v := range strings.Fields(s.options["subnet"]) {
			p, err := netip.ParsePrefix(strings.TrimPrefix(v, "!"))
			if err != nil {
				if a, aerr := netip.ParseAddr(strings.TrimPrefix(v, "!")); aerr == nil {
					p = netip.PrefixFrom(a, a.BitLen())
				}
			}
			if strings.HasPrefix(v, "!") || !p.IsValid() || !p.Addr().Is4() || p.Bits() < minLocalBits {
				return firstNonEmpty(s.options["name"], s.id)
			}
		}
	}
	return ""
}

// configuredPrefixes are the IPv4 networks the configuration holds, up or
// not: static interface addresses and static routes.
func configuredPrefixes(network []uciSection) []netip.Prefix {
	var out []netip.Prefix
	add := func(addr, mask string) {
		if p, err := netip.ParsePrefix(addr); err == nil {
			out = append(out, p.Masked())
			return
		}
		a, err := netip.ParseAddr(addr)
		if err != nil || !a.Is4() {
			return
		}
		bits := 32
		if m := net.ParseIP(mask).To4(); m != nil {
			bits, _ = net.IPMask(m).Size()
		}
		out = append(out, netip.PrefixFrom(a, bits).Masked())
	}
	for _, s := range network {
		switch s.kind {
		case "interface":
			for _, a := range strings.Fields(s.options["ipaddr"]) {
				add(a, s.options["netmask"])
			}
		case "route":
			add(s.options["target"], s.options["netmask"])
		}
	}
	return out
}

// zoneSubnets are the address ranges other zones are bound by (`subnet`):
// fw4 sends a packet from such a range into that zone, possibly before the
// guests' own. The guest subnet is chosen clear of them; one that covers it
// later is a weakened protection (review of #58).
func zoneSubnets(firewall []uciSection) map[string][]netip.Prefix {
	out := map[string][]netip.Prefix{}
	for _, s := range firewall {
		if s.kind != "zone" || s.id == guestSection {
			continue
		}
		for _, v := range strings.Fields(s.options["subnet"]) {
			if strings.HasPrefix(v, "!") {
				continue // see subnetHazard: a negation is refused, not avoided
			}
			p, err := netip.ParsePrefix(v)
			if err != nil {
				a, aerr := netip.ParseAddr(v)
				if aerr != nil {
					continue
				}
				p = netip.PrefixFrom(a, a.BitLen())
			}
			name := firstNonEmpty(s.options["name"], s.id)
			out[name] = append(out[name], p.Masked())
		}
	}
	return out
}

// guestWeakened names the first protection of the panel's own guest network
// that is no longer as the panel made it, "" when all hold. Any of them can be
// changed on the firewall screen or in LuCI, and the card must not keep
// promising what no longer holds (review of #58).
func (m *wifiManager) guestWeakened(st guestState) string {
	closed := func(v string) bool { return strings.EqualFold(v, "REJECT") || strings.EqualFold(v, "DROP") }
	byID := map[string]uciSection{}
	for _, s := range st.firewall {
		byID[s.id] = s
	}
	zone, ok := byID[guestSection]
	switch {
	case !ok || zone.kind != "zone":
		return "firewall." + guestSection
	case !closed(zone.options["input"]):
		return "firewall." + guestSection + ".input"
	case !closed(zone.options["forward"]):
		return "firewall." + guestSection + ".forward"
	case zone.options["name"] != guestZone || zone.options["network"] != guestSection:
		return "firewall." + guestSection + ".network"
	}
	// Nothing else on the zone: `family` would leave the other address
	// family outside its REJECT, `device`/`subnet` would change what it holds
	// (review of #58).
	for k := range zone.options {
		switch k {
		case "name", "network", "input", "output", "forward":
		default:
			return "firewall." + guestSection + "." + k
		}
	}
	// The guests' bridge exactly as made: an extra port (a LAN socket added in
	// LuCI) is not isolated from the guests (review of #58).
	for _, s := range st.network {
		if s.id != guestDevSection {
			continue
		}
		for k, v := range s.options {
			want := map[string]string{"name": guestBridge, "type": "bridge", "bridge_empty": "1"}
			if w, ok := want[k]; !ok || w != v {
				return "network." + guestDevSection + "." + k
			}
		}
	}
	// The guest zone exactly as made: bound by network, not by device.
	if zone.options["device"] != "" || zone.options["subnet"] != "" {
		return "firewall." + guestSection + ".device"
	}
	for _, s := range st.network {
		if otherOnGuestBridge(s) {
			return "network." + s.id
		}
	}
	if name := subnetHazard(st.firewall); name != "" {
		return "firewall." + name + ".subnet"
	}
	// Another zone bound to a range that covers the guests' subnet.
	// The whole guest subnet, not only the router's address in it: a zone on
	// its upper half takes the guests there (review of #58).
	if gp := configuredPrefixes([]uciSection{{kind: "interface", options: map[string]string{
		"ipaddr": guestOpt4(st.network, "ipaddr"), "netmask": firstNonEmpty(guestOpt4(st.network, "netmask"), "255.255.255.0"),
	}}}); len(gp) == 1 {
		for name, ps := range zoneSubnets(st.firewall) {
			for _, p := range ps {
				if p.Overlaps(gp[0]) {
					return "firewall." + name + ".subnet"
				}
			}
		}
	}
	// Guests reach the internet through one enabled forwarding to the
	// uplink's zone; without it the card would promise internet the zone's
	// REJECT takes away.
	fwd, ok := byID["guest_wan"]
	if !ok || fwd.kind != "forwarding" || fwd.options["src"] != guestZone || fwd.options["enabled"] == "0" {
		return "firewall.guest_wan"
	}
	if w, _ := m.uplinkZone(st.firewall, st.ifaces); fwd.options["dest"] != w {
		return "firewall.guest_wan.dest"
	}
	// The guest interface or its bridge in another zone too (moved into lan
	// in LuCI): that zone's policies would apply to guests.
	if name := zoneTakingGuests(st.firewall); name != "" {
		return "firewall." + name
	}
	wan, _ := m.uplinkZone(st.firewall, st.ifaces)
	ours := map[string]bool{}
	for _, r := range guestRuleSpecs(wan) {
		ours[r.id] = true
		if s, ok := byID[r.id]; !ok || !r.matches(s) {
			return "firewall." + r.id
		}
	}
	// Anything else that lets guests in.
	if name := guestOpenings(st, wan, ours); name != "" {
		return "firewall." + name
	}
	for _, s := range st.shape.owned {
		if s.options["network"] != guestSection {
			return "wireless." + s.id + ".network"
		}
	}
	if len(st.shape.stray) > 0 {
		s := st.shape.stray[0]
		return "wireless." + firstNonEmpty(s.options["ssid"], s.id)
	}
	for _, s := range st.shape.ifaces {
		if s.options["isolate"] != "1" {
			return "wireless." + s.id + ".isolate"
		}
		if s.options["bridge_isolate"] != "1" {
			return "wireless." + s.id + ".bridge_isolate"
		}
	}
	return ""
}

// guestStatus is the `guest` object of GET /wifi.
// ifnames are the running access points by section (network.wireless
// status): on the air is what hostapd says, not what the settings - possibly
// an unapplied draft - say (review of #58).
func (m *wifiManager) guestStatus(ctx context.Context, st guestState, ifnames map[string]string) *core.WiFiGuest {
	if !m.guestSupported(st.cfg) {
		return nil
	}
	ipv6, renumber := guestIPv6(st.network, delegatedPrefixes(st.ifaces))
	g := &core.WiFiGuest{State: core.GuestNone, IPv6: ipv6, RenumbersHome: renumber}
	g.Blocked, g.BlockedBy = m.guestBlocker(st)
	switch {
	case st.shape.ours && len(st.shape.owned) > 0:
		// The panel's own, even with every access point moved off the guest
		// network in LuCI: the card stays, with its warning and "Turn off".
		g.State, g.RenumbersHome = core.GuestOff, false
		shown := st.shape.ifaces
		if len(shown) == 0 {
			shown = st.shape.owned
		}
		if (guestShape{ifaces: shown}).enabled() {
			g.State = core.GuestOn
		}
		for _, s := range shown {
			if ifname, ok := ifnames[s.id]; ok {
				if state, _ := m.apState(ctx, ifname); state == core.RadioUp {
					g.OnAir = true
				}
			}
		}
		g.Network = shown[0].id
	case st.shape.ours:
		// The panel's own with no access point left at all (removed in
		// LuCI): off, and removable - not someone else's (review of #58).
		g.State, g.RenumbersHome = core.GuestOff, false
	case st.shape.foreign:
		g.State, g.RenumbersHome = core.GuestForeign, false
	default:
		// Removed in the draft, still on the air until it is applied: the
		// card must not say "none" over access points that are broadcasting.
		for sec, ifname := range ifnames {
			if strings.HasPrefix(sec, guestPrefix) {
				if state, _ := m.apState(ctx, ifname); state == core.RadioUp {
					g.OnAir = true
				}
			}
		}
	}
	return g
}

// uplinkZone is the firewall zone the uplink is in: guests are forwarded to it.
// The uplink is asked of WANInfo, which already knows to prefer the interface
// called wan over a tunnel with a default route of its own; without the bus
// (tests) the first default route stands in.
func (m *wifiManager) uplinkZone(firewall []uciSection, ifaces []ubus.Interface) (string, []string) {
	uplink := ""
	uplink = pickUplink(ifaces)
	for _, s := range firewall {
		if nets := strings.Fields(s.options["network"]); s.kind == "zone" && uplink != "" && contains(nets, uplink) {
			return s.options["name"], nets
		}
	}
	if uplink != "" {
		// Known but in no zone by its network (a `wwan` next to an unused
		// stock `wan` zone): "wan" would be another connection's zone.
		return "", nil
	}
	for _, s := range firewall {
		if s.kind == "zone" && s.options["name"] == "wan" {
			return "wan", strings.Fields(s.options["network"])
		}
	}
	// No uplink known and no zone called wan: there is nothing to forward
	// guests to. Inventing "wan" would write rules for a zone that does not
	// exist and leave guests without internet (review of #58).
	return "", nil
}

// dhcpSkipsGuests names the dnsmasq restriction that leaves the guests out:
// an `interface` list without them, or a `notinterface` list with them (by
// network or by bridge). "" when dnsmasq serves them.
func dhcpSkipsGuests(dhcp []uciSection) string {
	for _, s := range dhcp {
		if s.kind != "dnsmasq" {
			continue
		}
		if only := strings.Fields(s.options["interface"]); len(only) > 0 && !contains(only, guestSection) && !contains(only, guestBridge) {
			return s.options["interface"]
		}
		if skip := strings.Fields(s.options["notinterface"]); contains(skip, guestSection) || contains(skip, guestBridge) {
			return s.options["notinterface"]
		}
	}
	return ""
}

// pickUplink chooses the uplink from the interface dump the caller already
// has, by WANInfo's rules (one interface with a default route; else the one
// called wan, else the only one with wan in its name; else the first) - and
// without a second dump on a timeout of its own, which a netifd that stopped
// answering would stretch past the caller's deadline (review of #58).
func pickUplink(ifaces []ubus.Interface) string {
	var cands []core.NetworkInterface
	for _, i := range ifaces {
		_, v4 := i.DefaultRoute()
		_, v6 := i.DefaultRoute6()
		if loopbackNames[i.Name] || !(v4 || v6) {
			continue
		}
		cands = append(cands, core.NetworkInterface{Name: i.Name})
	}
	switch {
	case len(cands) == 0:
		return ""
	case len(cands) == 1:
		return cands[0].Name
	}
	if c, ok := pickByName(cands); ok {
		return c.Name
	}
	return cands[0].Name
}

// discard drops the draft of the keys a failed guest edit touched - those
// keys only, so nothing else waiting in the draft goes with them. It gets a
// context of its own: the edit may have failed BECAUSE its context ran out,
// and a revert run under that context would not run at all, leaving half a
// guest network in the draft for the next apply to commit.
func (m *wifiManager) discard(cause error, keys ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), stageTimeout)
	defer cancel()
	errs := []error{cause}
	seen := map[string]bool{}
	for _, k := range keys {
		if seen[k] {
			continue
		}
		seen[k] = true
		if err := m.net.revertKey(ctx, k); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// sectionOf is "config.section" of a key that may name an option.
func sectionOf(key string) string {
	parts := strings.SplitN(key, ".", 3)
	if len(parts) < 2 {
		return key
	}
	return parts[0] + "." + parts[1]
}

// StageGuest stages a new guest network on the given radios (every radio
// when none are given).
func (m *wifiManager) StageGuest(cfg core.GuestConfig) ([]core.ConfigChange, error) {
	if err := core.ValidSSID(cfg.SSID); err != nil {
		return nil, err
	}
	// wifi-scripts take a WPA network with an empty key without a word: the
	// access point comes up and nobody can join (D-105). So it is checked here.
	if err := core.ValidWiFiPassword(cfg.Password); err != nil {
		return nil, err
	}
	sec := cfg.Security
	if sec == "" {
		sec = core.WiFiWPA2WPA3
	}
	enc := encryptionFor(sec)
	if enc == "" {
		return nil, core.Refuse("security", fmt.Errorf("openwrt: %q is not offered (D-104)", cfg.Security))
	}
	ctx, cancel := context.WithTimeout(context.Background(), stageTimeout)
	defer cancel()
	st, err := m.readGuestState(ctx)
	if err != nil {
		return nil, err
	}
	if !m.guestSupported(st.cfg) {
		return nil, core.ErrNotImplemented
	}
	if st.shape.ours || st.shape.foreign {
		return nil, core.ErrGuestExists
	}
	var radios []string
	for _, id := range cfg.Radios {
		if !contains(radios, id) {
			radios = append(radios, id)
		}
	}
	if len(radios) == 0 {
		for _, r := range st.cfg.radios {
			// Every radio but 6 GHz: it allows WPA3 only, which the panel does
			// not offer (D-104) - hostapd would refuse the access point.
			if bandWord(r.options["band"]) != "6" {
				radios = append(radios, r.id)
			}
		}
	}
	if len(radios) == 0 {
		return nil, core.Refuse("radios", errors.New("openwrt: this device has no radio a guest network can use (6 GHz allows WPA3 only, D-104)"))
	}
	for _, id := range radios {
		if _, ok := st.cfg.radio(id); !ok {
			return nil, core.Refuse("radios", fmt.Errorf("openwrt: no radio %q on this device", id))
		}
		if r, _ := st.cfg.radio(id); bandWord(r.options["band"]) == "6" {
			return nil, core.Refuse("radios", fmt.Errorf("openwrt: 6 GHz allows WPA3 only, which a guest network here does not offer (D-104)"))
		}
		// An access point names its radio, and its own section is named after
		// it: an unnamed radio (`@wifi-device[0]`) can be neither (review of #58).
		if !sectionNameRe.MatchString(id) {
			return nil, core.Refuse("radios", fmt.Errorf("openwrt: radio %q has no name in the configuration; name it (radio0, for example) to put a guest network on it", id))
		}
	}
	if st.ifacesErr != nil {
		return nil, fmt.Errorf("openwrt: the router's networks could not be read, so a free one for guests cannot be chosen: %w", st.ifacesErr)
	}
	// Guests are forwarded to the uplink's zone. When the home network is in
	// that same zone (one of our own stands is built so), the forwarding
	// would hand guests the home network itself.
	if kind, by := m.guestBlocker(st); kind != "" {
		return nil, fmt.Errorf("%w: %s %q", core.ErrGuestBlocked, kind, by)
	}
	wan, _ := m.uplinkZone(st.firewall, st.ifaces)
	var kernel []netip.Prefix
	for _, r := range m.net.kernelRoutes() {
		kernel = append(kernel, r.prefix)
	}
	for _, ps := range zoneSubnets(st.firewall) {
		kernel = append(kernel, ps...)
	}
	// What is configured but not up right now (an interface that is down, a
	// static route on it) is taken too: it comes back (review of #58).
	kernel = append(kernel, configuredPrefixes(st.network)...)
	subnet, err := freeGuestSubnet(st.ifaces, kernel)
	if err != nil {
		return nil, err
	}
	ip, _, _ := net.ParseCIDR(subnet)
	ip = ip.To4()
	ip[3] = 1
	_, renumber := guestIPv6(st.network, delegatedPrefixes(st.ifaces))
	lanAssign := ""
	for _, s := range st.network {
		if s.id == lanSection {
			lanAssign = s.options["ip6assign"]
		}
	}

	type kv struct{ key, value string }
	sets := []kv{
		{"network." + guestDevSection, "device"},
		{"network." + guestDevSection + ".name", guestBridge},
		{"network." + guestDevSection + ".type", "bridge"},
		{"network." + guestDevSection + ".bridge_empty", "1"},
		{"network." + guestSection, "interface"},
		{"network." + guestSection + ".device", guestBridge},
		{"network." + guestSection + ".proto", "static"},
		{"network." + guestSection + ".ipaddr", ip.String()},
		{"network." + guestSection + ".netmask", "255.255.255.0"},
		{"network." + guestSection + ".ip6assign", strconv.Itoa(guestAssign)},
	}
	if renumber {
		sets = append(sets, kv{"network." + lanSection + ".ip6assign", strconv.Itoa(guestAssign)})
	}
	for _, id := range radios {
		w := "wireless." + guestPrefix + id
		sets = append(sets,
			kv{w, "wifi-iface"}, kv{w + ".device", id}, kv{w + ".mode", "ap"},
			kv{w + ".network", guestSection}, kv{w + ".ssid", cfg.SSID},
			kv{w + ".encryption", enc}, kv{w + ".key", cfg.Password},
			kv{w + ".isolate", "1"}, kv{w + ".bridge_isolate", "1"})
	}
	d := "dhcp." + guestSection
	sets = append(sets,
		kv{d, "dhcp"}, kv{d + ".interface", guestSection}, kv{d + ".start", "100"},
		kv{d + ".limit", "150"}, kv{d + ".leasetime", "2h"}, kv{d + ".dhcpv4", "server"},
		kv{d + ".dhcpv6", "server"}, kv{d + ".ra", "server"}, kv{d + ".ra_slaac", "1"})
	f := "firewall."
	z := f + guestSection
	sets = append(sets,
		kv{z, "zone"}, kv{z + ".name", guestZone}, kv{z + ".network", guestSection},
		kv{z + ".input", "REJECT"}, kv{z + ".output", "ACCEPT"}, kv{z + ".forward", "REJECT"},
		kv{f + "guest_wan", "forwarding"}, kv{f + "guest_wan.src", guestZone},
		kv{f + "guest_wan.dest", wan})
	lists := []kv{}
	for _, r := range guestRuleSpecs(wan) {
		sets = append(sets, kv{f + r.id, "rule"}, kv{f + r.id + ".name", r.name})
		for _, o := range r.opts {
			sets = append(sets, kv{f + r.id + "." + o.key, o.value})
		}
		for _, l := range r.lists {
			for _, v := range l.values {
				lists = append(lists, kv{f + r.id + "." + l.key, v})
			}
		}
	}

	// What a failure takes back: every section this edit writes, and the
	// home network's ip6assign - not the files.
	var touched []string
	for _, s := range sets {
		if s.key == "network."+lanSection+".ip6assign" {
			touched = append(touched, s.key)
		} else {
			touched = append(touched, sectionOf(s.key))
		}
	}
	for _, s := range sets {
		if err := m.net.uciSet(ctx, s.key, s.value); err != nil {
			return nil, m.discard(err, touched...)
		}
	}
	for _, l := range lists {
		if _, err := m.net.run(ctx, "uci", "add_list", l.key+"="+l.value); err != nil {
			return nil, m.discard(fmt.Errorf("openwrt: stage %s: %w", l.key, err), touched...)
		}
	}

	// The row says what the draft makes, worded the same way StagedChanges
	// words it after a reload.
	made := guestShape{ours: true}
	for _, id := range radios {
		made.ifaces = append(made.ifaces, uciSection{
			id: guestPrefix + id, kind: "wifi-iface",
			options: map[string]string{"device": id, "network": guestSection, "ssid": cfg.SSID},
		})
	}
	out := []core.ConfigChange{guestRow("", guestWords(made, st.cfg.radios))}
	if renumber {
		out = append(out, lanAssignRow(lanAssign, strconv.Itoa(guestAssign)))
	}
	return out, nil
}

// StageGuestEnabled turns the guest network's access points off or on. Its
// name, password and everything else stay.
func (m *wifiManager) StageGuestEnabled(enabled bool) ([]core.ConfigChange, error) {
	ctx, cancel := context.WithTimeout(context.Background(), stageTimeout)
	defer cancel()
	st, err := m.readGuestState(ctx)
	if err != nil {
		return nil, err
	}
	if !st.shape.ours || len(st.shape.owned) == 0 {
		return nil, core.ErrNoGuest
	}
	before := guestWords(st.shape, st.cfg.radios)
	after := st.shape
	after.ifaces = nil
	var touched []string
	type flip struct{ key, from, to string }
	var flips []flip
	// Off is every access point under the panel's names, one moved to lan in
	// LuCI included: it would keep broadcasting the guests' name and password
	// with the home network behind it (review of #58). On is only those still
	// on the guest network.
	for _, s := range st.shape.owned {
		key := "wireless." + s.id + ".disabled"
		on := s.options["disabled"] != "1"
		attached := s.options["network"] == guestSection
		switch {
		case enabled && !on && attached:
			touched = append(touched, key)
			flips = append(flips, flip{key, s.options["disabled"], ""})
			if err := m.net.uciDelete(ctx, key); err != nil {
				return nil, m.discard(err, touched...)
			}
		case !enabled && on:
			touched = append(touched, key)
			flips = append(flips, flip{key, s.options["disabled"], "1"})
			if err := m.net.uciSet(ctx, key, "1"); err != nil {
				return nil, m.discard(err, touched...)
			}
		}
		if !attached {
			continue
		}
		opts := map[string]string{}
		for k, v := range s.options {
			opts[k] = v
		}
		if enabled {
			delete(opts, "disabled")
		} else {
			opts["disabled"] = "1"
		}
		after.ifaces = append(after.ifaces, uciSection{id: s.id, kind: s.kind, options: opts})
	}
	to := guestWords(after, st.cfg.radios)
	switch {
	case to != before:
		return []core.ConfigChange{guestRow(before, to)}, nil
	case len(flips) > 0:
		// The network as a whole reads the same (one access point moved off
		// it, or one of several turned back on): a row per access point, with
		// the values the draft really holds - as it will read after a reload.
		var out []core.ConfigChange
		for _, f := range flips {
			said := describe("wireless", roleWiFiNet, "disabled")
			out = append(out, core.ConfigChange{Label: said.words, LabelKey: said.key, From: f.from, To: f.to,
				Dangerous: dangerousConfig("wireless"), Detail: f.key})
		}
		return out, nil
	}
	return []core.ConfigChange{}, nil
}

// StageGuestRemove removes every section of the panel's guest network. The
// home network's IPv6 numbering is NOT put back: that would change the home
// devices' addresses a second time (D-106).
func (m *wifiManager) StageGuestRemove() ([]core.ConfigChange, error) {
	ctx, cancel := context.WithTimeout(context.Background(), stageTimeout)
	defer cancel()
	st, err := m.readGuestState(ctx)
	if err != nil {
		return nil, err
	}
	if !st.shape.ours {
		return nil, core.ErrNoGuest
	}
	before := guestWords(st.shape, st.cfg.radios)
	var keys []string
	for _, s := range st.shape.owned {
		keys = append(keys, "wireless."+s.id)
	}
	// Only what is there: after an emergency restore just the access points
	// are left (see readGuestShape).
	netHas := map[string]bool{}
	for _, s := range st.network {
		netHas[s.id] = true
	}
	for _, id := range []string{guestSection, guestDevSection} {
		if netHas[id] {
			keys = append(keys, "network."+id)
		}
	}
	// Presence from the read that succeeded, not from a second call whose
	// failure would read as "absent" and leave the section behind - after
	// which the panel could not make a guest network again (review of #58).
	for _, s := range st.dhcp {
		if s.id == guestSection {
			keys = append(keys, "dhcp."+guestSection)
		}
	}
	// Only what is there: a section removed by hand must not make the panel
	// unable to remove the rest.
	have := map[string]bool{}
	for _, s := range st.firewall {
		have[s.id] = true
	}
	for _, r := range append([]string{guestSection}, guestRules...) {
		if have[r] {
			keys = append(keys, "firewall."+r)
		}
	}
	for i, k := range keys {
		if err := m.net.uciDelete(ctx, k); err != nil {
			return nil, m.discard(err, keys[:i+1]...)
		}
	}
	return []core.ConfigChange{guestRow(before, "")}, nil
}

// guestDraft is the guest network before and after the draft, in apply-bar
// words.
type guestDraft struct {
	ours      bool
	was, will string
	shown     bool
	// The guest sections on disk and in the draft, as "config.section".
	before, after map[string]bool
}

// folds says whether one line of the draft is part of the guest network
// appearing, going away, or being turned on or off - the edits that are the
// one row. Anything else touching a guest section (a guest rule disabled on
// the firewall screen, an option changed in LuCI) keeps its own row: folding
// it would hide a firewall change from the person confirming it.
func (g guestDraft) folds(e stagedEdit) bool {
	if g.was == g.will {
		return false
	}
	sec := e.config + "." + e.section
	if (g.was == "" || g.will == "") && g.before[sec] != g.after[sec] {
		return true
	}
	return e.config == "wireless" && strings.HasPrefix(e.section, guestPrefix) && e.option == "disabled"
}

// guestDraft fails closed: a read that fails would make a pending guest
// network look like none, and the draft guard (no other edits while one
// waits) would let a whole-file revert split it (review of #58).
func (m networkManager) guestDraft(ctx context.Context) (guestDraft, error) {
	var readErr error
	// A router without radios has no wireless file at all: that is no Wi-Fi,
	// not an unreadable draft (review of #58).
	dir := m.configDir
	if dir == "" {
		dir = "/etc/config"
	}
	_, statErr := os.Stat(filepath.Join(dir, "wireless"))
	noWireless := errors.Is(statErr, os.ErrNotExist)
	read := func(committed bool) (guestShape, []uciSection, map[string]bool) {
		show := map[string][]uciSection{}
		for _, c := range []string{"network", "firewall", "wireless", "dhcp"} {
			var out []byte
			var err error
			if committed {
				err = m.withCommitted(c, func(dir, alias string) error {
					var e error
					out, e = m.run(ctx, "uci", "-q", "-c", dir, "show", alias)
					return e
				})
			} else {
				out, err = m.run(ctx, "uci", "-q", "show", c)
			}
			if err != nil && readErr == nil && !(c == "wireless" && noWireless) {
				readErr = fmt.Errorf("openwrt: read %s for the guest network: %w", c, err)
			}
			show[c] = parseUCISections(string(out))
		}
		var radios []uciSection
		for _, s := range show["wireless"] {
			if s.kind == "wifi-device" {
				radios = append(radios, s)
			}
		}
		secs := map[string]bool{}
		for c, list := range show {
			for _, s := range list {
				if isGuestSection(c, s.id) {
					secs[c+"."+s.id] = true
				}
			}
		}
		return readGuestShape(show["network"], show["firewall"], show["wireless"], show["dhcp"]), radios, secs
	}
	before, radiosBefore, secsBefore := read(true)
	after, radiosAfter, secsAfter := read(false)
	if readErr != nil {
		return guestDraft{}, readErr
	}
	return guestDraft{
		ours:   before.ours || after.ours,
		was:    guestWords(before, radiosBefore),
		will:   guestWords(after, radiosAfter),
		before: secsBefore, after: secsAfter,
	}, nil
}

var _ core.WiFiGuestManager = (*wifiManager)(nil)
