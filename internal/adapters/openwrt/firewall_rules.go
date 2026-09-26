package openwrt

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/veilbridge-os/veilbridge/internal/core"
)

// Staging the owner's own traffic rules (M3.3, #35): add, edit, switch off,
// remove. Same rules as the port forwards next door — stage only, the
// device's firewall has the last word (D-66), every change is dangerous
// (D-69), and the rules firewall4 ships are read-only (D-67).
//
// Two things are particular to rules:
//
//   - ORDER. The firewall acts on the first rule that matches. Measured on the
//     router: the owner's rule allowing tcp 22 and 8080 from the internet side
//     sits above everything added later, so a new "block 8080" appended at
//     the end would never act — and the panel would have said "done". A new
//     rule can therefore be placed in front of an existing one (D-70), and
//     one that is certain never to act where it lands is refused (D-71).
//   - CONDITIONS THE PANEL DOES NOT SHOW (source address, schedule, …). A rule
//     that has them is switched on and off or removed, never edited here.

// ruleTargets are the panel's actions as firewall4 spells them.
var ruleTargets = map[string]string{
	core.ActionAccept: "ACCEPT",
	core.ActionReject: "REJECT",
	core.ActionDrop:   "DROP",
}

// StageRule validates and stages one rule of the owner's.
func (m networkManager) StageRule(cfg core.FirewallRuleConfig) ([]core.ConfigChange, error) {
	if m.run == nil {
		return nil, core.ErrNotImplemented
	}
	want, err := normalizeRule(cfg)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), stageTimeout)
	defer cancel()

	fw, err := m.FirewallInfo()
	if err != nil {
		return nil, err
	}
	zones := map[string]bool{"*": true}
	for _, z := range fw.Zones {
		zones[z.Name] = true
	}
	if !zones[want.From] {
		return nil, core.Refuse("from", fmt.Errorf("openwrt: there is no zone %q on this device", want.From))
	}
	if want.To != "" && !zones[want.To] {
		return nil, core.Refuse("to", fmt.Errorf("openwrt: there is no zone %q on this device", want.To))
	}

	section := strings.TrimSpace(cfg.ID)
	fresh := section == ""
	subject := ""  // which rule an edit is about, as it is now
	position := -1 // index in the whole file for `uci reorder`; -1 = last
	if fresh {
		earlier := fw.Rules
		if before := strings.TrimSpace(cfg.Before); before != "" {
			at := ruleIndex(fw, before)
			if at < 0 {
				return nil, core.Refuse("before", fmt.Errorf("openwrt: no rule %q on this device", before))
			}
			earlier = fw.Rules[:at]
			if position, err = m.sectionPosition(ctx, before); err != nil {
				return nil, err
			}
		}
		if cover := shadowedBy(earlier, want); cover != nil {
			return nil, core.Refuse("before", fmt.Errorf(
				"openwrt: the rule %s above already decides this traffic, so this one would never act; place it before that rule",
				ruleName(*cover)))
		}
	} else {
		if strings.TrimSpace(cfg.Before) != "" {
			return nil, core.Refuse("before", fmt.Errorf("openwrt: an existing rule cannot be moved yet"))
		}
		if !sectionNameRe.MatchString(section) && !anonSectionRe.MatchString(section) {
			return nil, core.Refuse("id", fmt.Errorf("openwrt: %q is not an entry on this device", section))
		}
		at := ruleIndex(fw, section)
		if at < 0 {
			return nil, core.Refuse("id", fmt.Errorf("openwrt: no rule %q on this device", section))
		}
		have := fw.Rules[at]
		subject = entrySubject(have.Name,
			ruleWords(have.From, have.To, strings.Join(have.Protocols, " "), have.Ports, have.Family))
		if have.System {
			return nil, core.Refuse("id", errSystemRule(have))
		}
		if len(have.Unsupported) > 0 && !sameButEnabled(have, want) {
			return nil, core.Refuse("id", fmt.Errorf(
				"openwrt: rule %s has conditions the panel does not show yet (%s); it can be switched on or off or removed here, and edited in LuCI",
				ruleName(have), strings.Join(have.Unsupported, ", ")))
		}
	}

	baseline := m.fw4Warnings(ctx)
	if fresh {
		out, err := m.run(ctx, "uci", "add", "firewall", "rule")
		if err != nil {
			return nil, fmt.Errorf("openwrt: stage a rule: %w", err)
		}
		section = strings.TrimSpace(string(out))
		if !sectionNameRe.MatchString(section) {
			_ = m.discardConfig(ctx, "firewall")
			return nil, fmt.Errorf("openwrt: the device named the new rule %q", section)
		}
	}

	d := func(option string) phrase { return describe("firewall", roleRule, option) }
	proto := strings.Join(want.Protocols, " ")
	sets := []wanSetting{
		{key: "src", value: want.From, label: d("src")},
		{key: "dest", value: want.To, remove: want.To == "", label: d("dest")},
		{key: "proto", value: proto, label: d("proto")},
		{key: "dest_port", value: want.Ports, remove: want.Ports == "", label: d("dest_port")},
		{key: "target", value: ruleTargets[want.Action], label: d("target")},
		// "any" is what LuCI writes for both families; it behaves like none.
		{key: "family", value: want.Family, remove: want.Family == "", sameAsAbsent: "any", label: d("family")},
		{key: "name", value: want.Name, remove: want.Name == "", label: d("name")},
	}
	if !fresh {
		// An existing rule may store the defaults by leaving them out: no
		// `proto` is TCP and UDP, no `target` is ACCEPT. Writing them out
		// would be a row in the apply bar that changes nothing.
		key := "firewall." + section + "."
		if raw := m.uciGet(ctx, key+"proto"); proto == "tcp udp" && (raw == "" || raw == "tcpudp") {
			sets = slices.DeleteFunc(sets, func(s wanSetting) bool { return s.key == "proto" })
		}
		if raw := m.uciGet(ctx, key+"target"); want.Action == core.ActionAccept && raw == "" {
			sets = slices.DeleteFunc(sets, func(s wanSetting) bool { return s.key == "target" })
		}
	}
	// Off is `enabled=0`; on is the option's absence, as for a port forward.
	if want.Enabled {
		sets = append(sets, wanSetting{key: "enabled", remove: true, sameAsAbsent: "1", label: d("enabled")})
	} else {
		sets = append(sets, wanSetting{key: "enabled", value: "0", label: d("enabled")})
	}

	changes, err := m.stage(ctx, "firewall", section, roleRule, sets)
	if err != nil {
		return nil, err
	}
	if fresh && position >= 0 {
		// Measured on 23.05.5: the index is the section's place in the WHOLE
		// file (defaults, zones and forwardings count), and a staged reorder
		// shows in `uci changes` as `firewall.cfg…='5'` — the same shape as
		// the line that adds the section, which is why the diff folds it into
		// the rule's one row.
		if _, err := m.run(ctx, "uci", "reorder", "firewall."+section+"="+strconv.Itoa(position)); err != nil {
			_ = m.discardConfig(ctx, "firewall")
			return nil, fmt.Errorf("openwrt: place the new rule: %w", err)
		}
	}
	if err := m.fw4Accepts(ctx, baseline); err != nil {
		_ = m.discardConfig(ctx, "firewall")
		return nil, err
	}
	if !fresh {
		return withSubject(changes, subject), nil
	}
	said := rulePhrase(ruleTargets[want.Action], want.To)
	return []core.ConfigChange{{
		Label:     said.words,
		LabelKey:  said.key,
		To:        ruleWords(want.From, want.To, proto, want.Ports, want.Family),
		Dangerous: dangerousConfig("firewall"),
		Detail:    "firewall." + section,
	}}, nil
}

// RemoveRule stages the removal of one of the owner's rules. Its refusals
// name no field: the id is in the path, there is nothing in a form to point
// at, the same as for removing a port forward.
func (m networkManager) RemoveRule(id string) ([]core.ConfigChange, error) {
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
	at := ruleIndex(fw, id)
	if at < 0 {
		return nil, fmt.Errorf("openwrt: no rule %q on this device", id)
	}
	r := fw.Rules[at]
	if r.System {
		return nil, errSystemRule(r)
	}
	ctx, cancel := context.WithTimeout(context.Background(), stageTimeout)
	defer cancel()
	if _, err := m.run(ctx, "uci", "delete", "firewall."+id); err != nil {
		return nil, fmt.Errorf("openwrt: stage removal of a rule: %w", err)
	}
	said := rulePhrase(r.Action, r.To)
	return []core.ConfigChange{{
		Label:     said.words,
		LabelKey:  said.key,
		From:      ruleWords(r.From, r.To, strings.Join(r.Protocols, " "), r.Ports, r.Family),
		Dangerous: dangerousConfig("firewall"),
		Detail:    "firewall." + id,
	}}, nil
}

// normalizeRule checks everything that can be checked without the device and
// returns the rule in the form it is written: protocols in a fixed order,
// ports separated by single spaces, the action in the panel's words.
func normalizeRule(cfg core.FirewallRuleConfig) (core.FirewallRuleConfig, error) {
	out := cfg
	out.Name = strings.TrimSpace(cfg.Name)
	if strings.ContainsAny(out.Name, "'\"\n\\") {
		return out, core.Refuse("name", fmt.Errorf("openwrt: a name cannot contain quotes, backslashes or line breaks"))
	}
	out.From = strings.TrimSpace(cfg.From)
	if out.From == "" {
		return out, core.Refuse("from", fmt.Errorf("openwrt: say which zone the traffic comes from"))
	}
	out.To = strings.TrimSpace(cfg.To)
	for field, zone := range map[string]string{"from": out.From, "to": out.To} {
		if zone != "" && zone != "*" && !sectionNameRe.MatchString(zone) {
			return out, core.Refuse(field, fmt.Errorf("openwrt: %q is not a zone name", zone))
		}
	}
	protos, err := ruleProtocols(cfg.Protocols)
	if err != nil {
		return out, core.Refuse("protocols", err)
	}
	out.Protocols = protos
	out.Ports = ""
	if strings.TrimSpace(cfg.Ports) != "" {
		if !portsApply(protos) {
			return out, core.Refuse("ports", fmt.Errorf("openwrt: ports only mean something for TCP and UDP"))
		}
		ports, _, ok := parsePortList(cfg.Ports)
		if !ok {
			return out, core.Refuse("ports", fmt.Errorf("openwrt: %q is not a port, a range, or a list of them", cfg.Ports))
		}
		out.Ports = ports
	}
	out.Action = strings.ToLower(strings.TrimSpace(cfg.Action))
	if _, ok := ruleTargets[out.Action]; !ok {
		return out, core.Refuse("action", fmt.Errorf("openwrt: %q is not an action; use accept, reject or drop", cfg.Action))
	}
	out.Family = strings.ToLower(strings.TrimSpace(cfg.Family))
	if out.Family != "" && out.Family != "ipv4" && out.Family != "ipv6" {
		return out, core.Refuse("family", fmt.Errorf("openwrt: %q is not ipv4 or ipv6", cfg.Family))
	}
	return out, nil
}

// ruleProtocols accepts any of TCP, UDP and ICMP, or "all" on its own.
func ruleProtocols(in []string) ([]string, error) {
	seen := map[string]bool{}
	for _, p := range in {
		p = strings.ToLower(strings.TrimSpace(p))
		switch p {
		case "tcp", "udp", "icmp", "all":
			seen[p] = true
		default:
			return nil, fmt.Errorf("openwrt: %q is not a protocol the panel can write a rule for", p)
		}
	}
	if seen["all"] {
		if len(seen) > 1 {
			return nil, fmt.Errorf("openwrt: \"all\" already includes every protocol")
		}
		return []string{"all"}, nil
	}
	var out []string
	for _, p := range []string{"tcp", "udp", "icmp"} {
		if seen[p] {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("openwrt: choose at least one protocol")
	}
	return out, nil
}

// portsApply reports whether a destination port means anything for these
// protocols: only TCP and UDP have ports.
func portsApply(protos []string) bool {
	for _, p := range protos {
		if p != "tcp" && p != "udp" {
			return false
		}
	}
	return len(protos) > 0
}

// parsePortList reads one port, a range, or several of them separated by
// spaces or commas — `22 8080` is what the owner's rule on the router holds.
// It returns them space-separated, the way firewall4 lists them.
func parsePortList(v string) (string, [][2]int, bool) {
	items := strings.FieldsFunc(v, func(r rune) bool { return r == ' ' || r == ',' || r == '\t' })
	if len(items) == 0 || len(items) > 32 {
		return "", nil, false
	}
	var ranges [][2]int
	for _, it := range items {
		first, last, ok := parsePorts(it)
		if !ok {
			return "", nil, false
		}
		ranges = append(ranges, [2]int{first, last})
	}
	return strings.Join(items, " "), ranges, true
}

// shadowedBy returns the first earlier rule that is certain to decide every
// packet the new one would match, and decides it the other way (D-71). Only
// certainty counts: an earlier rule with conditions the panel cannot read, or
// one that is switched off, never shadows — a false refusal would be worse
// than the silence this check exists to end.
func shadowedBy(earlier []core.FirewallRule, want core.FirewallRuleConfig) *core.FirewallRule {
	if !want.Enabled {
		return nil
	}
	for i := range earlier {
		r := earlier[i]
		if !r.Enabled || len(r.Unsupported) > 0 || r.Action == core.ActionOther {
			continue
		}
		if (r.Action == core.ActionAccept) == (want.Action == core.ActionAccept) {
			continue // same outcome: redundant, not silenced
		}
		if covers(r, want) {
			return &earlier[i]
		}
	}
	return nil
}

// covers reports whether rule r matches every packet the requested rule would.
func covers(r core.FirewallRule, want core.FirewallRuleConfig) bool {
	if r.From != want.From && r.From != "*" {
		return false
	}
	if r.To != want.To && (r.To != "*" || want.To == "") {
		return false
	}
	if r.Family != "" && r.Family != "any" && r.Family != want.Family {
		return false
	}
	if !slices.Contains(r.Protocols, "all") {
		if slices.Contains(want.Protocols, "all") {
			return false
		}
		for _, p := range want.Protocols {
			if !slices.Contains(r.Protocols, p) {
				return false
			}
		}
	}
	if r.Ports == "" {
		return true
	}
	if want.Ports == "" || !portsApply(want.Protocols) {
		return false
	}
	_, have, ok := parsePortList(r.Ports)
	if !ok {
		return false
	}
	_, need, _ := parsePortList(want.Ports)
	for _, n := range need {
		if !rangeCovered(n, have) {
			return false
		}
	}
	return true
}

// rangeCovered reports whether every port of n is in one of the ranges.
func rangeCovered(n [2]int, ranges [][2]int) bool {
	for p := n[0]; p <= n[1]; p++ {
		in := false
		for _, r := range ranges {
			if p >= r[0] && p <= r[1] {
				in = true
				break
			}
		}
		if !in {
			return false
		}
	}
	return true
}

// sameButEnabled reports whether a request changes nothing about a rule but
// whether it is on.
func sameButEnabled(have core.FirewallRule, want core.FirewallRuleConfig) bool {
	haveProtos, err := ruleProtocols(have.Protocols)
	if err != nil {
		return false
	}
	havePorts := have.Ports
	if havePorts != "" {
		if p, _, ok := parsePortList(havePorts); ok {
			havePorts = p
		}
	}
	return have.Name == want.Name && have.From == want.From && have.To == want.To &&
		slices.Equal(haveProtos, want.Protocols) && havePorts == want.Ports &&
		have.Action == want.Action && strings.TrimPrefix(have.Family, "any") == want.Family
}

func ruleIndex(fw core.FirewallStatus, id string) int {
	for i, r := range fw.Rules {
		if r.ID == id {
			return i
		}
	}
	return -1
}

// sectionPosition is the place of a section in the whole firewall file, which
// is what `uci reorder` counts in.
func (m networkManager) sectionPosition(ctx context.Context, id string) (int, error) {
	out, err := m.run(ctx, "uci", "-q", "show", "firewall")
	if err != nil {
		return 0, fmt.Errorf("openwrt: read the firewall: %w", err)
	}
	for i, s := range parseUCISections(string(out)) {
		if s.id == id {
			return i, nil
		}
	}
	return 0, core.Refuse("before", fmt.Errorf("openwrt: no rule %q on this device", id))
}

func errSystemRule(r core.FirewallRule) error {
	return fmt.Errorf("openwrt: rule %s ships with the firewall and is shown read-only", ruleName(r))
}

func ruleName(r core.FirewallRule) string {
	if r.Name != "" {
		return strconv.Quote(r.Name)
	}
	return r.ID
}

// ruleWords names a rule the way a person reads it: "wan · tcp 22, 8080",
// "lan → wan · tcp+udp 443 · IPv6". What the rule DOES is in the label
// (rulePhrase); a rule for every protocol has no protocol part at all.
func ruleWords(src, dest, proto, ports, family string) string {
	where := src
	if dest != "" {
		where = src + " → " + dest
	}
	parts := []string{where}
	if p := protocols(proto); !(len(p) == 1 && p[0] == "all") {
		s := strings.Join(p, "+")
		if ports != "" {
			s += " " + strings.Join(strings.Fields(ports), ", ")
		}
		parts = append(parts, s)
	}
	switch family {
	case "ipv4":
		parts = append(parts, "IPv4")
	case "ipv6":
		parts = append(parts, "IPv6")
	}
	return strings.Join(parts, " · ")
}
