package openwrt

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/veilbridge-os/veilbridge/internal/routing"
	"github.com/veilbridge-os/veilbridge/internal/vpn"
)

// tunnelRouter puts the active connection into the kernel's routing (#47,
// D-82, D-107). The connection gets its own table instead of the main one,
// which stays netifd's (D-73), and five rules ahead of main decide who uses it:
//
//   - the engine's own encrypted socket (vpn.BypassMark) → main, so the
//     packets carrying the tunnel never enter it;
//   - traffic marked for the tunnel (routing.TunnelMark) → the whole table:
//     a rule sends an address into the tunnel even over a static route (D-73);
//   - marked traffic that found no route there → a table that refuses it;
//   - everything else → main without its default route, so the router's own
//     networks and static routes win over the connection's networks: a
//     connection that lists the LAN, or the whole internet in two halves,
//     must not take the router's answers to its own clients;
//   - everything → the table without its default route
//     (suppress_prefixlength 0), so the connection's networks are reachable
//     from the whole LAN without a rule, and only marked traffic exits by it.
//
// The refusal is what a connection that is down looks like: its table is
// empty, and without the refusal marked traffic would quietly fall through to
// the uplink (measured, D-107). It sits right after the marked rule, before
// main: placed after, marked traffic to an address in the router's own
// networks took main's route and left directly (measured, #47). Like a Keenetic policy that holds one
// connection, traffic sent to it does not go anywhere else.
//
// Everything here is plain `ip`, which BusyBox provides on every OpenWrt
// branch; table numbers stay below 256 because BusyBox refuses larger ones.
type tunnelRouter struct {
	// mu serialises every change: node activation and applying the routing
	// rules both reach Ensure, under different managers' locks, and two
	// rebuilds sharing one hold would take each other's refusal away.
	mu  sync.Mutex
	run commandRunner
	// hasIPv6 reports whether the kernel has IPv6; without it, the IPv6 half
	// is skipped rather than failed.
	hasIPv6 func() bool
}

// tunnelMarkValue is routing.TunnelMark as a number, for SO_MARK.
const tunnelMarkValue = 0x40000000

const (
	ipProgram = "/sbin/ip"

	tunnelTable = "147"
	refuseTable = "148"

	prioHold   = "30089"
	prioBypass = "30090"
	prioMarked = "30100"
	prioRefuse = "30101"
	prioLocal  = "30102"
	prioNets   = "30103"
)

func newTunnelRouter(run commandRunner) *tunnelRouter {
	return &tunnelRouter{run: run, hasIPv6: kernelHasIPv6}
}

func kernelHasIPv6() bool {
	_, err := os.Stat("/proc/sys/net/ipv6")
	return err == nil
}

func (r *tunnelRouter) families() []string {
	if r.hasIPv6() {
		return []string{"-4", "-6"}
	}
	return []string{"-4"}
}

func (r *tunnelRouter) ip(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return r.run(ctx, ipProgram, args...)
}

// Ensure puts the rules and the refusing table in place. Rules already in
// place as expected are left alone, so activating or switching a node never
// takes the refusal away. When they have to be rebuilt (after a reboot, or a
// rule of somebody else on one of these priorities), a temporary refusal
// holds marked traffic for the duration: between deleting and re-adding, it
// would otherwise leave by the uplink.
func (r *tunnelRouter) Ensure() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ensureLocked()
}

func (r *tunnelRouter) ensureLocked() error {
	var errs []error
	for _, f := range r.families() {
		want := r.wantRules()
		if r.inPlace(f, want) {
			// A hold left by an interrupted rebuild would refuse marked
			// traffic for good while the tunnel reads as up; the permanent
			// refusal is verified, so it can go.
			if err := r.release(f); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		errs = append(errs, r.rebuild(f, want)...)
	}
	return errors.Join(errs...)
}

// wantRules is the rule set in priority order, as argument lists for
// `ip rule add`; the first element of each is its priority.
func (r *tunnelRouter) wantRules() [][]string {
	marked := routing.TunnelMark + "/" + routing.TunnelMark
	bypass := fmt.Sprintf("0x%x/0x%x", vpn.BypassMark, vpn.BypassMark)
	return [][]string{
		{prioBypass, "fwmark", bypass, "lookup", "main"},
		{prioMarked, "fwmark", marked, "lookup", tunnelTable},
		{prioRefuse, "fwmark", marked, "lookup", refuseTable},
		{prioLocal, "lookup", "main", "suppress_prefixlength", "0"},
		{prioNets, "lookup", tunnelTable, "suppress_prefixlength", "0"},
	}
}

// inPlace reports whether the kernel holds exactly the wanted rules on the
// panel's priorities and the refusing route. `ip rule show` prints a rule as
// "30100:	from all fwmark … lookup 147" (BusyBox adds a trailing space).
func (r *tunnelRouter) inPlace(f string, want [][]string) bool {
	out, err := r.ip(f, "rule", "show")
	if err != nil {
		return false
	}
	ours := map[string]bool{}
	for _, w := range want {
		ours[w[0]] = true
	}
	var have []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.Join(strings.Fields(line), " ")
		prio, _, ok := strings.Cut(line, ":")
		if ok && ours[prio] {
			have = append(have, line)
		}
	}
	if len(have) != len(want) {
		return false
	}
	for i, w := range want {
		if have[i] != w[0]+": from all "+strings.Join(w[1:], " ") {
			return false
		}
	}
	routes, err := r.ip(f, "route", "show", "table", refuseTable)
	return err == nil && strings.Contains(string(routes), "unreachable default")
}

// rebuild replaces the rules under a hold: marked traffic is refused from
// before the first rule goes until the new set is verified in place. Without
// the hold nothing is touched; if the new set does not verify, the hold stays
// the tunnel then refuses rather than leaks until the next attempt.
func (r *tunnelRouter) rebuild(f string, want [][]string) []error {
	marked := routing.TunnelMark + "/" + routing.TunnelMark
	if _, err := r.ip(f, "route", "replace", "unreachable", "default", "table", refuseTable); err != nil {
		return []error{err}
	}
	// A hold left by an earlier attempt may be the only refusal there is;
	// keep it rather than open a gap by replacing it.
	if !r.hasRule(f, prioHold, "fwmark "+marked+" lookup "+refuseTable) {
		if _, err := r.ip(f, "rule", "add", "fwmark", marked, "lookup", refuseTable, "priority", prioHold); err != nil {
			return []error{fmt.Errorf("hold marked traffic before rebuilding the rules: %w", err)}
		}
	}
	var errs []error
	for _, w := range want {
		r.deleteAll(f, w[0])
	}
	for _, w := range want {
		if _, err := r.ip(append(append([]string{f, "rule", "add"}, w[1:]...), "priority", w[0])...); err != nil {
			errs = append(errs, err)
		}
	}
	if !r.inPlace(f, want) {
		return append(errs, fmt.Errorf("the tunnel's rules (%s) did not verify; marked traffic stays refused", f))
	}
	if err := r.release(f); err != nil {
		errs = append(errs, err)
	}
	return errs
}

// release takes the hold out and checks it is gone: a hold that stays would
// refuse all marked traffic while the tunnel reads as up.
func (r *tunnelRouter) release(f string) error {
	out, err := r.ip(f, "rule", "show")
	if err != nil {
		return err
	}
	if !holdsPriority(string(out), prioHold) {
		return nil
	}
	r.deleteAll(f, prioHold)
	out, err = r.ip(f, "rule", "show")
	if err != nil {
		return err
	}
	if holdsPriority(string(out), prioHold) {
		return fmt.Errorf("the temporary hold (%s, priority %s) could not be removed; marked traffic stays refused", f, prioHold)
	}
	return nil
}

func holdsPriority(ruleShow, prio string) bool {
	for _, line := range strings.Split(ruleShow, "\n") {
		if p, _, ok := strings.Cut(strings.TrimSpace(line), ":"); ok && p == prio {
			return true
		}
	}
	return false
}

// hasRule reports whether priority prio holds exactly one rule, the given one.
func (r *tunnelRouter) hasRule(f, prio, rule string) bool {
	out, err := r.ip(f, "rule", "show")
	if err != nil {
		return false
	}
	var have []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if p, _, ok := strings.Cut(line, ":"); ok && p == prio {
			have = append(have, line)
		}
	}
	return len(have) == 1 && have[0] == prio+": from all "+rule
}

// deleteAll removes every rule on a priority; a priority is the panel's alone.
// Bounded, so a refusal to delete cannot spin.
func (r *tunnelRouter) deleteAll(f, prio string) {
	for range 8 {
		if _, err := r.ip(f, "rule", "del", "priority", prio); err != nil {
			return
		}
	}
}

// Up routes the connection on dev: its networks, and a default route for each
// family whose networks include everything (an exit). A connection that lists
// only some networks (a VPN to work) gets no default: marked traffic to
// other addresses is refused instead of leaving through it.
func (r *tunnelRouter) Up(dev string, allowed []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureLocked(); err != nil {
		return err
	}
	r.flush()
	if len(allowed) == 0 {
		allowed = []string{"0.0.0.0/0", "::/0"}
	}
	prefixes, errs := tunnelPrefixes(allowed)
	v6 := r.hasIPv6()
	for _, p := range prefixes {
		f := "-4"
		if p.Addr().Is6() {
			if !v6 {
				continue
			}
			f = "-6"
		}
		dst := p.String()
		if p.Bits() == 0 {
			dst = "default"
		}
		if _, err := r.ip(f, "route", "replace", dst, "dev", dev, "table", tunnelTable); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Down empties the connection's table. The rules stay: with the table empty,
// marked traffic reaches the refusal, which is the point.
func (r *tunnelRouter) Down() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flush()
	return nil
}

func (r *tunnelRouter) flush() {
	for _, f := range r.families() {
		// An empty table answers flush with an error on some builds; there is
		// nothing to undo either way.
		_, _ = r.ip(f, "route", "flush", "table", tunnelTable)
	}
}

// tunnelPrefixes parses a connection's networks. The whole address space
// written as two halves (0.0.0.0/1 + 128.0.0.0/1, ::/1 + 8000::/1, a common
// way to say "everything" without a default route) becomes the default
// route: it is an exit like any other, for marked traffic only; otherwise
// such a connection would quietly take all of the LAN's traffic.
func tunnelPrefixes(allowed []string) ([]netip.Prefix, []error) {
	var (
		out  []netip.Prefix
		errs []error
	)
	seen := map[netip.Prefix]bool{}
	for _, a := range allowed {
		p, err := netip.ParsePrefix(strings.TrimSpace(a))
		if err != nil {
			errs = append(errs, fmt.Errorf("tunnel route %q: %w", a, err))
			continue
		}
		p = p.Masked()
		seen[p] = true
		out = append(out, p)
	}
	halves := []struct{ lo, hi, all netip.Prefix }{
		{netip.MustParsePrefix("0.0.0.0/1"), netip.MustParsePrefix("128.0.0.0/1"), netip.MustParsePrefix("0.0.0.0/0")},
		{netip.MustParsePrefix("::/1"), netip.MustParsePrefix("8000::/1"), netip.MustParsePrefix("::/0")},
	}
	for _, h := range halves {
		if !seen[h.lo] || !seen[h.hi] {
			continue
		}
		out = slices.DeleteFunc(out, func(p netip.Prefix) bool { return p == h.lo || p == h.hi })
		if !seen[h.all] {
			out = append(out, h.all)
		}
	}
	return out, errs
}

// lanDevices names the kernel devices of fw4's "lan" zone: its networks,
// resolved through netifd, plus devices the zone lists directly. It reads the
// committed firewall, the one in force: a draft staged in the panel must not
// change whose traffic is marked before it is applied. These are the
// interfaces whose traffic a routing rule may send into the tunnel (#47): the
// same zone the package's firewall include lets into it.
func (m networkManager) lanDevices() ([]string, error) {
	if m.run == nil {
		return nil, fmt.Errorf("openwrt: cannot read the firewall on this build")
	}
	ctx, cancel := context.WithTimeout(context.Background(), firewallTimeout)
	defer cancel()
	var out []byte
	err := m.withCommitted("firewall", func(dir, alias string) error {
		var e error
		out, e = m.run(ctx, "uci", "-q", "-c", dir, "show", alias)
		return e
	})
	if err != nil {
		return nil, err
	}
	var networks, devices []string
	for _, s := range parseUCISections(string(out)) {
		if s.kind == "zone" && s.options["name"] == "lan" {
			networks = append(networks, strings.Fields(s.options["network"])...)
			devices = append(devices, strings.Fields(s.options["device"])...)
		}
	}
	if len(networks) > 0 {
		list := m.lookupInterfaces
		if list == nil {
			list = m.Interfaces
		}
		ifaces, err := list()
		if err != nil {
			return nil, err
		}
		for _, n := range networks {
			for _, i := range ifaces {
				if i.Name == n && i.Device != "" {
					devices = append(devices, i.Device)
				}
			}
		}
	}
	slices.Sort(devices)
	devices = slices.Compact(devices)
	if len(devices) == 0 {
		// Deliberately narrow: the package's firewall include lives in the
		// lan zone's chain (forward_lan), a static file fw4 picks up. A zone
		// under another name would need that file written at run time and
		// fw4 reloaded, which would also put a pending draft live. So the
		// panel says what it needs instead of marking traffic nothing lets
		// into the tunnel.
		return nil, fmt.Errorf("openwrt: routing into the tunnel needs the local network in a firewall zone named \"lan\" (OpenWrt's default), and this router has no such zone with an interface")
	}
	return devices, nil
}
