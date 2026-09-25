package openwrt

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/veilbridge-os/veilbridge/internal/core"
)

// Reading the firewall (M3.3, #35). The shapes here were captured from both
// stands (23.05.5 x86, 25.12.5 arm64): the stock configuration is identical on
// the two branches, which is what D-67 relies on.

const firewallTimeout = 5 * time.Second

// FirewallInfo reads the firewall configuration as it is committed plus any
// draft — `uci show` sees staged changes, and the panel shows what an apply
// would make live, the same as every other screen.
func (m networkManager) FirewallInfo() (core.FirewallStatus, error) {
	if m.run == nil {
		return core.FirewallStatus{}, core.ErrNotImplemented
	}
	ctx, cancel := context.WithTimeout(context.Background(), firewallTimeout)
	defer cancel()
	out, err := m.run(ctx, "uci", "-q", "show", "firewall")
	if err != nil {
		return core.FirewallStatus{}, err
	}
	// Which connections are up decides whether a zone is in use (D-68). A
	// failure to ask is not fatal: every zone then reads as not live, which
	// is the cautious answer, not a wrong one.
	up := map[string]bool{}
	list := m.lookupInterfaces
	if list == nil {
		list = m.Interfaces
	}
	if ifaces, err := list(); err == nil {
		for _, i := range ifaces {
			up[i.Name] = i.Up
		}
	}
	return parseFirewall(string(out), up), nil
}

// uciSection is one section of `uci show` output, in file order.
type uciSection struct {
	id      string // "@rule[3]" or a named section
	kind    string // "rule", "zone", "redirect", …
	options map[string]string
}

// parseUCISections groups `uci show <config>` lines by section. List values
// come out space-joined by unquoteUCI, which is safe for the lists read here
// (zone networks, protocols, ICMP types): none of their items contain spaces.
func parseUCISections(out string) []uciSection {
	var order []string
	byID := map[string]*uciSection{}
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		parts := strings.SplitN(key, ".", 3)
		switch len(parts) {
		case 2:
			if _, seen := byID[parts[1]]; !seen {
				order = append(order, parts[1])
				byID[parts[1]] = &uciSection{id: parts[1], options: map[string]string{}}
			}
			byID[parts[1]].kind = unquoteUCI(value)
		case 3:
			s, ok := byID[parts[1]]
			if !ok {
				continue
			}
			s.options[parts[2]] = unquoteUCI(value)
		}
	}
	out2 := make([]uciSection, 0, len(order))
	for _, id := range order {
		out2 = append(out2, *byID[id])
	}
	return out2
}

func parseFirewall(show string, up map[string]bool) core.FirewallStatus {
	st := core.FirewallStatus{
		Zones:        []core.FirewallZone{},
		Forwardings:  []core.ZoneForwarding{},
		PortForwards: []core.PortForward{},
		Rules:        []core.FirewallRule{},
	}
	system := stockRules()
	for _, s := range parseUCISections(show) {
		o := s.options
		switch s.kind {
		case "zone":
			z := core.FirewallZone{
				Name:       o["name"],
				Networks:   strings.Fields(o["network"]),
				Input:      action(o["input"]),
				Output:     action(o["output"]),
				Forward:    action(o["forward"]),
				Masquerade: o["masq"] == "1",
			}
			if z.Networks == nil {
				z.Networks = []string{}
			}
			for _, n := range z.Networks {
				if up[n] {
					z.Live = true
				}
				if n == lanSection {
					z.Role = core.ZoneLocal
				}
			}
			// NAT is what makes a zone the internet side. It wins over the
			// local network being in the same zone: that combination means
			// one flat zone, and hiding its NAT would be the bigger lie.
			if z.Masquerade {
				z.Role = core.ZoneInternet
			}
			st.Zones = append(st.Zones, z)
		case "forwarding":
			st.Forwardings = append(st.Forwardings, core.ZoneForwarding{From: o["src"], To: o["dest"]})
		case "redirect":
			// Only destination NAT is a port forward. A source-NAT redirect
			// is something else entirely, and calling it a forwarded port
			// would be wrong on the one screen people open to find out
			// what is reachable from outside.
			if t := o["target"]; t != "" && t != "DNAT" {
				continue
			}
			pf := core.PortForward{
				ID:           s.id,
				Name:         o["name"],
				Enabled:      o["enabled"] != "0",
				Protocols:    protocols(o["proto"]),
				From:         o["src"],
				ExternalPort: o["src_dport"],
				ToAddress:    o["dest_ip"],
				ToPort:       o["dest_port"],
			}
			if pf.ToPort == "" {
				pf.ToPort = pf.ExternalPort
			}
			st.PortForwards = append(st.PortForwards, pf)
		case "rule":
			st.Rules = append(st.Rules, core.FirewallRule{
				ID:        s.id,
				Name:      o["name"],
				Enabled:   o["enabled"] != "0",
				System:    system[canonical(o)],
				From:      o["src"],
				To:        o["dest"],
				Protocols: protocols(o["proto"]),
				Ports:     o["dest_port"],
				Action:    action(o["target"]),
				Family:    o["family"],
			})
		}
	}
	return st
}

// action turns ACCEPT/REJECT/DROP into the panel's words. An empty target on
// a rule means ACCEPT in firewall4, and so does an empty zone policy.
func action(target string) string {
	switch strings.ToUpper(target) {
	case "REJECT":
		return core.ActionReject
	case "DROP":
		return core.ActionDrop
	default:
		return core.ActionAccept
	}
}

// protocols reads the `proto` option; firewall4 treats its absence as both TCP
// and UDP, and "tcpudp" is the old spelling of the same thing. "all" is kept
// as it is: it means every protocol, not just those two.
func protocols(proto string) []string {
	f := strings.Fields(strings.ToLower(proto))
	if len(f) == 0 || (len(f) == 1 && f[0] == "tcpudp") {
		return []string{"tcp", "udp"}
	}
	return f
}

// canonical is a rule's options in a stable order, so a rule can be compared
// with the stock one by content (D-67).
func canonical(o map[string]string) string {
	keys := make([]string, 0, len(o))
	for k := range o {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "=" + o[k] + "\n")
	}
	return b.String()
}

// stockRules are the rules the firewall4 package ships, by content. Captured
// with `uci show firewall` on a clean OpenWrt 23.05.5 and a clean 25.12.5; the
// two were identical. A rule equal to one of these is shown read-only; one a
// person has changed is not "the system's" any more (D-67).
func stockRules() map[string]bool {
	set := map[string]bool{}
	for _, s := range parseUCISections(stockFirewallRules) {
		set[canonical(s.options)] = true
	}
	return set
}

const stockFirewallRules = `firewall.@rule[0]=rule
firewall.@rule[0].name='Allow-DHCP-Renew'
firewall.@rule[0].src='wan'
firewall.@rule[0].proto='udp'
firewall.@rule[0].dest_port='68'
firewall.@rule[0].target='ACCEPT'
firewall.@rule[0].family='ipv4'
firewall.@rule[1]=rule
firewall.@rule[1].name='Allow-Ping'
firewall.@rule[1].src='wan'
firewall.@rule[1].proto='icmp'
firewall.@rule[1].icmp_type='echo-request'
firewall.@rule[1].family='ipv4'
firewall.@rule[1].target='ACCEPT'
firewall.@rule[2]=rule
firewall.@rule[2].name='Allow-IGMP'
firewall.@rule[2].src='wan'
firewall.@rule[2].proto='igmp'
firewall.@rule[2].family='ipv4'
firewall.@rule[2].target='ACCEPT'
firewall.@rule[3]=rule
firewall.@rule[3].name='Allow-DHCPv6'
firewall.@rule[3].src='wan'
firewall.@rule[3].proto='udp'
firewall.@rule[3].dest_port='546'
firewall.@rule[3].family='ipv6'
firewall.@rule[3].target='ACCEPT'
firewall.@rule[4]=rule
firewall.@rule[4].name='Allow-MLD'
firewall.@rule[4].src='wan'
firewall.@rule[4].proto='icmp'
firewall.@rule[4].src_ip='fe80::/10'
firewall.@rule[4].icmp_type='130/0' '131/0' '132/0' '143/0'
firewall.@rule[4].family='ipv6'
firewall.@rule[4].target='ACCEPT'
firewall.@rule[5]=rule
firewall.@rule[5].name='Allow-ICMPv6-Input'
firewall.@rule[5].src='wan'
firewall.@rule[5].proto='icmp'
firewall.@rule[5].icmp_type='echo-request' 'echo-reply' 'destination-unreachable' 'packet-too-big' 'time-exceeded' 'bad-header' 'unknown-header-type' 'router-solicitation' 'neighbour-solicitation' 'router-advertisement' 'neighbour-advertisement'
firewall.@rule[5].limit='1000/sec'
firewall.@rule[5].family='ipv6'
firewall.@rule[5].target='ACCEPT'
firewall.@rule[6]=rule
firewall.@rule[6].name='Allow-ICMPv6-Forward'
firewall.@rule[6].src='wan'
firewall.@rule[6].dest='*'
firewall.@rule[6].proto='icmp'
firewall.@rule[6].icmp_type='echo-request' 'echo-reply' 'destination-unreachable' 'packet-too-big' 'time-exceeded' 'bad-header' 'unknown-header-type'
firewall.@rule[6].limit='1000/sec'
firewall.@rule[6].family='ipv6'
firewall.@rule[6].target='ACCEPT'
firewall.@rule[7]=rule
firewall.@rule[7].name='Allow-IPSec-ESP'
firewall.@rule[7].src='wan'
firewall.@rule[7].dest='lan'
firewall.@rule[7].proto='esp'
firewall.@rule[7].target='ACCEPT'
firewall.@rule[8]=rule
firewall.@rule[8].name='Allow-ISAKMP'
firewall.@rule[8].src='wan'
firewall.@rule[8].dest='lan'
firewall.@rule[8].dest_port='500'
firewall.@rule[8].proto='udp'
firewall.@rule[8].target='ACCEPT'
`

// The API asks for this by type assertion; if the adapter stopped satisfying
// it, the screen would silently answer 501 instead of failing the build.
var _ core.FirewallReader = networkManager{}
