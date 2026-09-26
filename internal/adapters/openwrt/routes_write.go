package openwrt

import (
	"context"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/veilbridge-os/veilbridge/internal/core"
)

// Staging static routes (M3.4, #37). Same rule as every writer here: stage
// only; the apply transaction commits under its confirmation window. Every
// check below exists because netifd would otherwise accept the route and the
// kernel would do something else — each was measured on both stands before it
// was written (DESIGN §0.9).

// roleRoute is a `route` section of `network`.
const roleRoute = "route"

// StageStaticRoute validates and stages one IPv4 route.
func (m networkManager) StageStaticRoute(cfg core.StaticRouteConfig) ([]core.ConfigChange, error) {
	if m.run == nil {
		return nil, core.ErrNotImplemented
	}
	ctx, cancel := context.WithTimeout(context.Background(), stageTimeout)
	defer cancel()

	name := strings.TrimSpace(cfg.Name)
	if strings.ContainsAny(name, "'\"\n\\") {
		return nil, core.Refuse("name", fmt.Errorf("openwrt: a name cannot contain quotes, backslashes or line breaks"))
	}
	if cfg.Metric < 0 || cfg.Metric > 65535 {
		return nil, core.Refuse("metric", fmt.Errorf("openwrt: a metric is a number from 0 to 65535"))
	}
	prefix, err := parseRouteTarget(cfg.Target)
	if err != nil {
		return nil, core.Refuse("target", err)
	}
	var gateway netip.Addr
	if g := strings.TrimSpace(cfg.Gateway); g != "" {
		gateway, err = netip.ParseAddr(g)
		if err != nil || !gateway.Is4() {
			return nil, core.Refuse("gateway", fmt.Errorf("openwrt: %q is not an IPv4 address", cfg.Gateway))
		}
	}

	out, err := m.run(ctx, "uci", "-q", "show", "network")
	if err != nil {
		return nil, err
	}
	sections, ifaceMetric := parseRouteSections(string(out))
	ifaces := m.routeInterfaces()

	section := strings.TrimSpace(cfg.ID)
	fresh := section == ""
	var current *routeSection
	if !fresh {
		if !sectionNameRe.MatchString(section) && !anonSectionRe.MatchString(section) {
			return nil, core.Refuse("id", fmt.Errorf("openwrt: %q is not an entry on this device", section))
		}
		for i := range sections {
			if sections[i].id == section {
				current = &sections[i]
			}
		}
		if current == nil {
			return nil, core.Refuse("id", fmt.Errorf("openwrt: no static route %q on this device", section))
		}
		if current.family != "ipv4" {
			return nil, core.Refuse("id", fmt.Errorf("openwrt: IPv6 routes can only be switched on and off or removed here"))
		}
	}

	iface, err := routeInterfaceFor(strings.TrimSpace(cfg.Interface), gateway, ifaces)
	if err != nil {
		return nil, err
	}

	if current != nil && len(current.unsupported) > 0 {
		// A route with options the panel does not show: only on/off. Editing
		// what nobody can see is how a route ends up doing something else.
		same := current.prefix == prefix && current.gateway == gateway &&
			current.iface == iface && current.metric == cfg.Metric && current.name == name
		if !same {
			return nil, core.Refuse("id", fmt.Errorf(
				"openwrt: this route has settings the panel does not show (%s); it can only be switched on and off or removed here",
				strings.Join(current.unsupported, ", ")))
		}
	}

	// D-76: a route the kernel already has for the same network and metric,
	// other than this route itself. netifd would take that route over and
	// remove it together with this one.
	metric := cfg.Metric
	if metric == 0 {
		metric = ifaceMetric[iface]
	}
	if cfg.Enabled {
		if err := m.routeClash(ctx, section, prefix, metric, sections, ifaceMetric, ifaces); err != nil {
			return nil, err
		}
	}

	if fresh {
		out, err := m.run(ctx, "uci", "add", "network", "route")
		if err != nil {
			return nil, fmt.Errorf("openwrt: stage a static route: %w", err)
		}
		section = strings.TrimSpace(string(out))
		if !sectionNameRe.MatchString(section) {
			_ = m.discardConfig(ctx, "network")
			return nil, fmt.Errorf("openwrt: the device named the new entry %q", section)
		}
	}

	d := func(option string) phrase { return describe("network", roleRoute, option) }
	sets := []wanSetting{{key: "interface", value: iface, label: d("interface")}}
	if current != nil && current.netmaskForm {
		// Keep the form the route was written in: switching it to CIDR would
		// put two rows in front of the person for a change they did not make.
		sets = append(sets,
			wanSetting{key: "target", value: prefix.Addr().String(), label: d("target")},
			wanSetting{key: "netmask", value: netmaskOf(prefix.Bits()), label: d("netmask")})
	} else {
		sets = append(sets,
			wanSetting{key: "target", value: prefix.String(), label: d("target")},
			wanSetting{key: "netmask", remove: true, label: d("netmask")})
	}
	if gateway.IsValid() {
		sets = append(sets, wanSetting{key: "gateway", value: gateway.String(), label: d("gateway")})
	} else {
		sets = append(sets, wanSetting{key: "gateway", remove: true, label: d("gateway")})
	}
	if cfg.Metric != 0 {
		sets = append(sets, wanSetting{key: "metric", value: strconv.Itoa(cfg.Metric), label: d("metric")})
	} else {
		sets = append(sets, wanSetting{key: "metric", remove: true, sameAsAbsent: "0", label: d("metric")})
	}
	if name != "" {
		sets = append(sets, wanSetting{key: "name", value: name, label: d("name")})
	} else {
		sets = append(sets, wanSetting{key: "name", remove: true, label: d("name")})
	}
	// Off is `disabled=1` (measured: netifd then keeps the route out of the
	// kernel on both branches); on is the option's absence.
	if cfg.Enabled {
		sets = append(sets, wanSetting{key: "disabled", remove: true, sameAsAbsent: "0", label: d("disabled")})
	} else {
		sets = append(sets, wanSetting{key: "disabled", value: "1", label: d("disabled")})
	}

	changes, err := m.stage(ctx, "network", section, roleRoute, sets)
	if err != nil {
		return nil, err
	}
	if !fresh {
		was := routeWords(current.prefix.String(), addrString(current.gateway), current.iface)
		return withSubject(changes, entrySubject(current.name, was)), nil
	}
	said := describe("network", roleRoute, "")
	return []core.ConfigChange{{
		Label:     said.words,
		LabelKey:  said.key,
		To:        routeWords(prefix.String(), addrString(gateway), iface),
		Dangerous: dangerousConfig("network"),
		Detail:    "network." + section,
	}}, nil
}

// RemoveStaticRoute stages the removal of one route, IPv4 or IPv6.
func (m networkManager) RemoveStaticRoute(id string) ([]core.ConfigChange, error) {
	if m.run == nil {
		return nil, core.ErrNotImplemented
	}
	if !sectionNameRe.MatchString(id) && !anonSectionRe.MatchString(id) {
		return nil, fmt.Errorf("openwrt: %q is not an entry on this device", id)
	}
	ctx, cancel := context.WithTimeout(context.Background(), stageTimeout)
	defer cancel()
	out, err := m.run(ctx, "uci", "-q", "show", "network")
	if err != nil {
		return nil, err
	}
	sections, _ := parseRouteSections(string(out))
	var target *routeSection
	for i := range sections {
		if sections[i].id == id {
			target = &sections[i]
		}
	}
	if target == nil {
		return nil, fmt.Errorf("openwrt: no static route %q on this device", id)
	}
	if _, err := m.run(ctx, "uci", "delete", "network."+id); err != nil {
		return nil, fmt.Errorf("openwrt: stage removal of a static route: %w", err)
	}
	said := describe("network", roleRoute, "")
	words := target.target
	if target.prefix.IsValid() {
		words = target.prefix.String()
	}
	return []core.ConfigChange{{
		Label:     said.words,
		LabelKey:  said.key,
		From:      routeWords(words, addrString(target.gateway), target.iface),
		Dangerous: dangerousConfig("network"),
		Detail:    "network." + id,
	}}, nil
}

// parseRouteTarget accepts an IPv4 network or a single address. Host bits are
// cleared, the way netifd clears them, so the diff shows the route the kernel
// will actually get.
func parseRouteTarget(v string) (netip.Prefix, error) {
	v = strings.TrimSpace(v)
	if p, err := netip.ParsePrefix(v); err == nil && p.Addr().Is4() {
		return p.Masked(), nil
	}
	if a, err := netip.ParseAddr(v); err == nil && a.Is4() {
		return netip.PrefixFrom(a, 32), nil
	}
	return netip.Prefix{}, fmt.Errorf("openwrt: %q is not an IPv4 network or address", v)
}

// routeInterfaceFor picks and checks the connection a route leaves through
// (D-75). The gateway has to be on that connection's network: otherwise the
// kernel refuses the route, netifd swallows the refusal and lists the route
// as present — measured on both stands. Only certainty refuses: a connection
// with no address known, or a point-to-point one (a /32, as PPPoE has), has no
// network to check against and is let through.
func routeInterfaceFor(name string, gateway netip.Addr, ifaces []routeInterface) (string, error) {
	if name == "" {
		if !gateway.IsValid() {
			return "", core.Refuse("interface", fmt.Errorf("openwrt: choose a connection, or give a gateway to pick it by"))
		}
		for _, i := range ifaces {
			if onNetwork(gateway, i.IPv4) {
				return i.Name, nil
			}
		}
		return "", core.Refuse("gateway", fmt.Errorf(
			"openwrt: %s is not on the network of any connection of this router", gateway))
	}
	if !sectionNameRe.MatchString(name) {
		return "", core.Refuse("interface", fmt.Errorf("openwrt: %q is not a connection on this device", name))
	}
	var found *routeInterface
	for i := range ifaces {
		if ifaces[i].Name == name {
			found = &ifaces[i]
		}
	}
	if found == nil {
		return "", core.Refuse("interface", fmt.Errorf("openwrt: no connection %q on this device", name))
	}
	if !gateway.IsValid() {
		return name, nil
	}
	for _, c := range found.IPv4 {
		if p, err := netip.ParsePrefix(c); err == nil && p.Addr() == gateway {
			return "", core.Refuse("gateway", fmt.Errorf("openwrt: %s is this router's own address on %s", gateway, name))
		}
	}
	if checkable(found.IPv4) && !onNetwork(gateway, found.IPv4) {
		return "", core.Refuse("gateway", fmt.Errorf(
			"openwrt: %s is not on the network of %s (%s); the device would accept the route and never use it",
			gateway, name, strings.Join(found.IPv4, ", ")))
	}
	return name, nil
}

// onNetwork reports whether an address lies in one of the given networks.
func onNetwork(a netip.Addr, cidrs []string) bool {
	for _, c := range cidrs {
		if p, err := netip.ParsePrefix(c); err == nil && p.Bits() < 32 && p.Masked().Contains(a) {
			return true
		}
	}
	return false
}

// checkable is true when a connection has a network a gateway can be checked
// against: an IPv4 address with room for another host on it.
func checkable(cidrs []string) bool {
	for _, c := range cidrs {
		if p, err := netip.ParsePrefix(c); err == nil && p.Addr().Is4() && p.Bits() < 32 {
			return true
		}
	}
	return false
}

// routeClash refuses a route whose network and metric the kernel already has
// from somebody else, or another configured route already claims (D-76). The
// route being edited is excluded by what the kernel holds for it now.
func (m networkManager) routeClash(
	ctx context.Context, section string, prefix netip.Prefix, metric int,
	sections []routeSection, ifaceMetric map[string]int, ifaces []routeInterface,
) error {
	devices := map[string]string{}
	names := map[string]string{}
	for _, i := range ifaces {
		devices[i.Name] = i.device
		if i.device != "" {
			names[i.device] = i.Name
		}
	}
	for _, r := range sections {
		if r.id == section || r.disabled || r.family != "ipv4" {
			continue
		}
		if r.prefix == prefix && r.effectiveMetric(ifaceMetric) == metric {
			return core.Refuse("target", fmt.Errorf(
				"openwrt: there is already a static route to %s with metric %d (%s); give this one another metric",
				prefix, metric, entrySubject(r.name, routeWords(r.prefix.String(), addrString(r.gateway), r.iface))))
		}
	}
	// What this route holds in the kernel right now, as committed — a draft
	// of it does not change the kernel until it is applied.
	var own []routeSection
	if section != "" {
		if committed, err := m.committedRouteSections(ctx); err == nil {
			for _, r := range committed {
				if r.id == section {
					own = append(own, r)
				}
			}
		}
	}
	for _, k := range m.kernelRoutes() {
		if k.prefix != prefix || k.metric != metric {
			continue
		}
		mine := false
		for _, r := range own {
			if !r.disabled && r.matches(k, devices[r.iface], ifaceMetric) {
				mine = true
			}
		}
		if mine {
			continue
		}
		owner := k.device
		if n, ok := names[k.device]; ok {
			owner = n
		}
		via := ""
		if k.gateway.IsValid() {
			via = " via " + k.gateway.String()
		}
		return core.Refuse("target", fmt.Errorf(
			"openwrt: the router already routes %s%s through %s with metric %d; the network service would take that route over and remove it with this one — give this one another metric",
			prefix, via, owner, metric))
	}
	return nil
}

// committedRouteSections reads the routes as they are on disk, without the
// draft — what the kernel was actually given.
func (m networkManager) committedRouteSections(ctx context.Context) ([]routeSection, error) {
	var sections []routeSection
	err := m.withCommitted("network", func(dir, alias string) error {
		out, err := m.run(ctx, "uci", "-q", "-c", dir, "show", alias)
		if err != nil {
			return err
		}
		sections, _ = parseRouteSections(string(out))
		return nil
	})
	return sections, err
}

// netmaskOf spells a prefix length as a dotted mask.
func netmaskOf(bits int) string {
	var m [4]byte
	for i := 0; i < bits; i++ {
		m[i/8] |= 0x80 >> (i % 8)
	}
	return netip.AddrFrom4(m).String()
}

func addrString(a netip.Addr) string {
	if !a.IsValid() {
		return ""
	}
	return a.String()
}

var _ core.RouteWriter = networkManager{}
