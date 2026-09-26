package openwrt

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"

	"github.com/veilbridge-os/veilbridge/internal/core"
)

// Staging port forwards (M3.3, #35). Same rule as every writer here: stage
// only; the apply transaction commits under its confirmation window, and every
// firewall change runs under it (D-69).

// fw4Program is pinned like reload_config: an unqualified name would resolve
// through PATH in a daemon that runs as root.
const fw4Program = "/sbin/fw4"

// portRe is one port or a first-last range, the two forms firewall4 accepts
// in `src_dport`/`dest_port` without a list.
var portRe = regexp.MustCompile(`^([0-9]{1,5})(?:-([0-9]{1,5}))?$`)

// parsePorts returns the first and last port of a single port or a range.
func parsePorts(v string) (int, int, bool) {
	m := portRe.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return 0, 0, false
	}
	first, _ := strconv.Atoi(m[1])
	last := first
	if m[2] != "" {
		last, _ = strconv.Atoi(m[2])
	}
	if first < 1 || last > 65535 || last < first {
		return 0, 0, false
	}
	return first, last, true
}

// StagePortForward validates and stages one port forward. The device's own
// firewall has the last word (D-66): after staging, `fw4 check` must not
// report anything it did not report before, or the draft is dropped.
func (m networkManager) StagePortForward(cfg core.PortForwardConfig) ([]core.ConfigChange, error) {
	if m.run == nil {
		return nil, core.ErrNotImplemented
	}
	protos, err := forwardProtocols(cfg.Protocols)
	if err != nil {
		return nil, core.Refuse("protocols", err)
	}
	extFirst, extLast, ok := parsePorts(cfg.ExternalPort)
	if !ok {
		return nil, core.Refuse("externalPort",
			fmt.Errorf("openwrt: %q is not a port or a range of ports", cfg.ExternalPort))
	}
	toPort := strings.TrimSpace(cfg.ToPort)
	if toPort != "" {
		first, last, ok := parsePorts(toPort)
		if !ok {
			return nil, core.Refuse("toPort", fmt.Errorf("openwrt: %q is not a port or a range of ports", cfg.ToPort))
		}
		// A range maps one to one; forwarding 100 ports into 3 is not a thing
		// the firewall does, it would silently take the first few.
		if last-first != extLast-extFirst {
			return nil, core.Refuse("toPort", fmt.Errorf(
				"openwrt: %s is %d port(s) and %s is %d", cfg.ExternalPort, extLast-extFirst+1, toPort, last-first+1))
		}
	}
	ip := net.ParseIP(strings.TrimSpace(cfg.ToAddress))
	if ip == nil || ip.To4() == nil {
		return nil, core.Refuse("toAddress", fmt.Errorf("openwrt: %q is not an IPv4 address", cfg.ToAddress))
	}
	name := strings.TrimSpace(cfg.Name)
	if strings.ContainsAny(name, "'\"\n\\") {
		return nil, core.Refuse("name", fmt.Errorf("openwrt: a name cannot contain quotes, backslashes or line breaks"))
	}

	ctx, cancel := context.WithTimeout(context.Background(), stageTimeout)
	defer cancel()

	// The device the connections go to has to be on the local network, or the
	// forward points into nowhere — and a forward into nowhere looks exactly
	// like a service that is down.
	if network, err := m.lanNetwork(ctx); err == nil && !network.Contains(ip) {
		return nil, core.Refuse("toAddress", fmt.Errorf("openwrt: %s is outside the local network %s", ip, network))
	}

	fw, err := m.FirewallInfo()
	if err != nil {
		return nil, err
	}
	from, into := "", ""
	for _, z := range fw.Zones {
		switch z.Role {
		case core.ZoneInternet:
			from = z.Name
		case core.ZoneLocal:
			into = z.Name
		}
	}
	if from == "" || into == "" {
		return nil, fmt.Errorf("openwrt: this firewall has no internet-side zone or no local zone to forward between")
	}

	section := strings.TrimSpace(cfg.ID)
	fresh := section == ""
	subject := "" // which forward an edit is about, as it is now
	if !fresh {
		if !sectionNameRe.MatchString(section) && !anonSectionRe.MatchString(section) {
			return nil, core.Refuse("id", fmt.Errorf("openwrt: %q is not an entry on this device", section))
		}
		if !hasPortForward(fw, section) {
			return nil, core.Refuse("id", fmt.Errorf("openwrt: no port forward %q on this device", section))
		}
		for _, pf := range fw.PortForwards {
			if pf.ID == section {
				subject = entrySubject(pf.Name, portForwardWords(
					strings.Join(pf.Protocols, " "), pf.ExternalPort, pf.ToAddress, pf.ToPort))
			}
		}
	}

	baseline := m.fw4Warnings(ctx)
	if fresh {
		out, err := m.run(ctx, "uci", "add", "firewall", "redirect")
		if err != nil {
			return nil, fmt.Errorf("openwrt: stage a port forward: %w", err)
		}
		section = strings.TrimSpace(string(out))
		if !sectionNameRe.MatchString(section) {
			_ = m.discardConfig(ctx, "firewall")
			return nil, fmt.Errorf("openwrt: the device named the new entry %q", section)
		}
	}

	d := func(option string) phrase { return describe("firewall", rolePortForward, option) }
	sets := []wanSetting{
		{key: "target", value: "DNAT", label: d("target")},
		{key: "src", value: from, label: d("src")},
		{key: "dest", value: into, label: d("dest")},
		{key: "proto", value: strings.Join(protos, " "), label: d("proto")},
		{key: "src_dport", value: strings.TrimSpace(cfg.ExternalPort), label: d("src_dport")},
		{key: "dest_ip", value: ip.String(), label: d("dest_ip")},
	}
	if toPort != "" {
		sets = append(sets, wanSetting{key: "dest_port", value: toPort, label: d("dest_port")})
	} else {
		sets = append(sets, wanSetting{key: "dest_port", remove: true, label: d("dest_port")})
	}
	if name != "" {
		sets = append(sets, wanSetting{key: "name", value: name, label: d("name")})
	}
	// Off is `enabled=0`; on is the option's absence, which is how firewall4
	// and LuCI write it — so switching back on removes it rather than
	// leaving a `1` behind that nothing else writes.
	if cfg.Enabled {
		sets = append(sets, wanSetting{key: "enabled", remove: true, sameAsAbsent: "1", label: d("enabled")})
	} else {
		sets = append(sets, wanSetting{key: "enabled", value: "0", label: d("enabled")})
	}

	changes, err := m.stage(ctx, "firewall", section, rolePortForward, sets)
	if err != nil {
		return nil, err
	}
	if err := m.fw4Accepts(ctx, baseline); err != nil {
		_ = m.discardConfig(ctx, "firewall")
		return nil, err
	}
	if !fresh {
		return withSubject(changes, subject), nil
	}
	said := describe("firewall", rolePortForward, "")
	return []core.ConfigChange{{
		Label:     said.words,
		LabelKey:  said.key,
		To:        portForwardWords(strings.Join(protos, " "), cfg.ExternalPort, ip.String(), toPort),
		Dangerous: dangerousConfig("firewall"),
		Detail:    "firewall." + section,
	}}, nil
}

// RemovePortForward stages the removal of one port forward.
func (m networkManager) RemovePortForward(id string) ([]core.ConfigChange, error) {
	if m.run == nil {
		return nil, core.ErrNotImplemented
	}
	if !sectionNameRe.MatchString(id) && !anonSectionRe.MatchString(id) {
		return nil, fmt.Errorf("openwrt: %q is not an entry on this device", id)
	}
	fw, err := m.FirewallInfo()
	if err != nil {
		return nil, err
	}
	var target *core.PortForward
	for _, pf := range fw.PortForwards {
		if pf.ID == id {
			found := pf
			target = &found
		}
	}
	if target == nil {
		return nil, fmt.Errorf("openwrt: no port forward %q on this device", id)
	}
	ctx, cancel := context.WithTimeout(context.Background(), stageTimeout)
	defer cancel()
	if _, err := m.run(ctx, "uci", "delete", "firewall."+id); err != nil {
		return nil, fmt.Errorf("openwrt: stage removal of a port forward: %w", err)
	}
	said := describe("firewall", rolePortForward, "")
	return []core.ConfigChange{{
		Label:     said.words,
		LabelKey:  said.key,
		From:      portForwardWords(strings.Join(target.Protocols, " "), target.ExternalPort, target.ToAddress, target.ToPort),
		Dangerous: dangerousConfig("firewall"),
		Detail:    "firewall." + id,
	}}, nil
}

func hasPortForward(fw core.FirewallStatus, id string) bool {
	for _, pf := range fw.PortForwards {
		if pf.ID == id {
			return true
		}
	}
	return false
}

// forwardProtocols accepts TCP, UDP or both, and nothing else: a forward of
// ICMP or "all" is not a port forward.
func forwardProtocols(in []string) ([]string, error) {
	var tcp, udp bool
	for _, p := range in {
		switch strings.ToLower(strings.TrimSpace(p)) {
		case "tcp":
			tcp = true
		case "udp":
			udp = true
		default:
			return nil, fmt.Errorf("openwrt: %q is not a protocol a port can be forwarded for", p)
		}
	}
	switch {
	case tcp && udp:
		return []string{"tcp", "udp"}, nil
	case tcp:
		return []string{"tcp"}, nil
	case udp:
		return []string{"udp"}, nil
	}
	return nil, fmt.Errorf("openwrt: choose TCP, UDP or both")
}

// portForwardWords names a port forward the way a person reads it:
// "tcp 8443 → 192.168.1.50:443".
func portForwardWords(proto, external, address, internal string) string {
	protos := protocols(proto)
	if internal == "" {
		internal = external
	}
	return fmt.Sprintf("%s %s → %s:%s", strings.Join(protos, "+"), external, address, internal)
}

// fw4Warnings returns the lines `fw4 check` prints about sections it will
// skip. Measured on 25.12.5: the check sees the uci draft, exits 0 even when a
// section is unusable, and says so only as `[!] Section … skipped` (D-66).
func (m networkManager) fw4Warnings(ctx context.Context) map[string]bool {
	out, _ := m.run(ctx, fw4Program, "check")
	warnings := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "[!]") {
			warnings[line] = true
		}
	}
	return warnings
}

// fw4Accepts refuses a draft that makes the firewall report something it did
// not report before. Warnings that were already there belong to somebody
// else's section and must not block this change.
func (m networkManager) fw4Accepts(ctx context.Context, baseline map[string]bool) error {
	var fresh []string
	for w := range m.fw4Warnings(ctx) {
		if !baseline[w] {
			fresh = append(fresh, w)
		}
	}
	if len(fresh) == 0 {
		return nil
	}
	return fmt.Errorf("openwrt: the firewall would skip this entry: %s", strings.Join(fresh, "; "))
}

var _ core.FirewallWriter = networkManager{}
