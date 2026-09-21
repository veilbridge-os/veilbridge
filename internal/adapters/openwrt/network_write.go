package openwrt

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/veilbridge-os/veilbridge/internal/core"
)

// The write half of the network manager (M3.1).
//
// Everything here stages and nothing here commits. `uci set` writes to the
// staging area only; the change becomes live when the apply transaction calls
// `uci commit` under a watchdog, and is undone by restoring the snapshot if
// nobody confirms. That is the whole safety model the M1 risk gate proved on
// real hardware, and it only holds while these two verbs stay apart.
//
// The uplink is also the most dangerous thing in the panel to edit: on the
// reference router it is the only way in. So this file refuses more than it
// accepts — a value that does not parse here never reaches the device, where
// the cost of a typo is a drive to the router with a cable.

// stageTimeout bounds a batch of uci calls.
const stageTimeout = 20 * time.Second

// sectionNameRe is what uci accepts as a section name. Interface names come
// from the device itself today, but they will come from a form tomorrow, and
// a name with a shell metacharacter or a dot would address a different key.
var sectionNameRe = regexp.MustCompile(`^[a-zA-Z0-9_]{1,32}$`)

// optionLabels is the ONE list of words the panel uses for a configuration
// key, shared by both directions: staging an edit here, and reading a draft
// back off the device after the page was reloaded. Two lists would drift, and
// the drift is invisible until an operator refreshes the browser — which is
// exactly how the raw key `network.wan.proto` reached the apply bar (M3.1a).
//
// Keyed by `<config>.<role>.<option>`. The role matters as much as the option
// name: `ipaddr` on the uplink is the address the provider handed us, and the
// same key on the local network is this router's own address. Until M3.2 the
// table was keyed by option alone — fine while only the uplink was editable,
// and a lie the moment the local network became editable too. The limit was
// written down at M3.1a and is paid off here rather than discovered by an
// operator reading "Address on the internet side" above a LAN address.
var optionLabels = map[string]string{
	"network.uplink.proto":    "Internet connection type",
	"network.uplink.ipaddr":   "Address on the internet side",
	"network.uplink.netmask":  "Network mask",
	"network.uplink.gateway":  "Gateway",
	"network.uplink.dns":      "Resolvers",
	"network.uplink.peerdns":  "Use the provider's resolvers",
	"network.uplink.username": "Provider login",
	"network.uplink.password": "Provider password",

	"network.lan.proto":   "How the local network address is set",
	"network.lan.ipaddr":  "Address of this router on the local network",
	"network.lan.netmask": "Local network mask",
	"network.lan.dns":     "Resolvers for the local network",

	"dhcp.lan.start":     "First address handed out",
	"dhcp.lan.limit":     "Last address handed out",
	"dhcp.lan.leasetime": "How long an address is given for",
	"dhcp.lan.ignore":    "Hand out addresses on the local network",

	"dhcp.host.mac":  "Device",
	"dhcp.host.ip":   "Reserved address",
	"dhcp.host.name": "Device name",
}

// Section roles: what the panel means by a section, as opposed to what the
// device happens to call it. The uplink is `wan` on most routers and `lanwan`
// on one of our own stands, so the name cannot be the role.
const (
	roleUplink = "uplink"
	roleLAN    = "lan"
	roleHost   = "host"
)

// configLabels name a whole configuration file in domain words, for a key we
// have no words for yet. Saying "a network setting" and keeping the key in
// `detail` is honest; printing `network.wan.metric` on screen is not (D-3).
var configLabels = map[string]string{
	"network":  "Network setting",
	"firewall": "Firewall setting",
	"dhcp":     "Local network setting",
	"wireless": "Wi-Fi setting",
	"system":   "Device setting",
}

// sectionLabels name a whole section, for a draft that adds or removes one.
// Removing a section in `network` removes an entire connection, which is the
// largest thing this diff can describe and must never be a blank row.
var sectionLabels = map[string]string{
	"network":  "Network connection",
	"firewall": "Firewall rule",
	"dhcp":     "Address handout",
	"wireless": "Wi-Fi network",
}

// labelFor turns a configuration key into what the panel calls it. An unknown
// role falls back to naming the configuration file rather than guessing: a
// wrong word is worse than a general one on a screen people act on.
func labelFor(config, role, option string) string {
	if option == "" {
		if role == roleHost {
			return "Reserved address"
		}
		if l, ok := sectionLabels[config]; ok {
			return l
		}
		return "Configuration section"
	}
	if l, ok := optionLabels[config+"."+role+"."+option]; ok {
		return l
	}
	if l, ok := configLabels[config]; ok {
		return l
	}
	return "System setting"
}

// secretOption reports whether a key holds something that must never be shown
// back, not even to an authenticated session reading it on a shared screen.
func secretOption(option string) bool {
	return option == "password" || option == "key"
}

// dangerousConfig marks the files where an edit can cut the panel's own
// access, which is what turns an apply into a confirm-or-be-reverted
// transaction. Renaming the device cannot lock anybody out; changing the
// network or the firewall can.
func dangerousConfig(config string) bool {
	return config == "network" || config == "firewall"
}

// StageWAN validates cfg and stages it. It returns the list of edits in the
// panel's own vocabulary, which is what the apply bar shows before anyone
// commits anything.
func (m networkManager) StageWAN(cfg core.WANConfig) ([]core.ConfigChange, error) {
	if m.run == nil {
		return nil, core.ErrNotImplemented
	}

	iface := cfg.Interface
	if iface == "" {
		wan, err := m.WANInfo()
		if err != nil {
			return nil, fmt.Errorf("openwrt: no interface given and no uplink found: %w", err)
		}
		iface = wan.Interface.Name
	}
	if !sectionNameRe.MatchString(iface) {
		return nil, fmt.Errorf("openwrt: %q is not a valid interface name", iface)
	}

	sets, err := wanSettings(cfg)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), stageTimeout)
	defer cancel()

	return m.stage(ctx, "network", iface, roleUplink, sets)
}

// stage writes a batch of settings into the draft of one section and returns
// them described the way the panel shows them. It is shared by the uplink and
// the local network on purpose: a second copy of these rules would be a second
// place for "staging accidentally commits" to creep back in.
func (m networkManager) stage(
	ctx context.Context, config, section, role string, sets []wanSetting,
) ([]core.ConfigChange, error) {
	_ = role // the words are already resolved by the caller; kept for clarity
	if !sectionNameRe.MatchString(section) {
		return nil, fmt.Errorf("openwrt: %q is not a valid section name", section)
	}

	// The "before" values are read first, so the diff describes this device
	// and not an assumption about it. A key that does not exist yet reads as
	// empty, which is exactly how it should appear in the diff.
	changes := make([]core.ConfigChange, 0, len(sets))
	for _, s := range sets {
		key := fmt.Sprintf("%s.%s.%s", config, section, s.key)
		before := m.uciGet(ctx, key)
		// Removing a key that is not there changes nothing, and asking uci to
		// do it fails; nor does removing one that already holds the value the
		// device assumes when it is absent. Setting a value it already holds
		// is not an edit either.
		removeIsNoop := s.remove && (before == "" || (s.sameAsAbsent != "" && before == s.sameAsAbsent))
		if removeIsNoop || (!s.remove && before == s.value) {
			continue
		}
		var stageErr error
		if s.remove {
			stageErr = m.uciDelete(ctx, key)
		} else {
			stageErr = m.uciSet(ctx, key, s.value)
		}
		if stageErr != nil {
			// A half-staged batch is not left behind: the draft is dropped so
			// the operator never confirms a change they did not see in full.
			_ = m.discardConfig(ctx, config)
			return nil, stageErr
		}

		// Some keys are stored as something the panel never says out loud —
		// the pool is offsets from the network address (D-44). Those settings
		// carry their own words for both columns.
		from, to := redact(s.secret, before), redact(s.secret, s.value)
		if s.shownBefore != "" {
			from = s.shownBefore
		}
		if s.shown != "" {
			to = s.shown
		}
		if s.remove {
			to = ""
		}
		changes = append(changes, core.ConfigChange{
			Label:     s.label,
			From:      from,
			To:        to,
			Dangerous: dangerousConfig(config),
			Detail:    key,
		})
	}
	return changes, nil
}

// wanSetting is one key to stage, with the words the panel uses for it.
type wanSetting struct {
	key    string
	value  string
	label  string
	secret bool
	// remove drops the key instead of writing it. A setting that can only be
	// turned on is a trap: it was measured on a live device that asking for
	// the provider's resolvers again produced no edits at all, so `peerdns=0`
	// and a hand-typed resolver stayed on the uplink forever (M3.4a).
	remove bool
	// sameAsAbsent is the written value that already behaves like no key at
	// all. Removing it would be a row in the apply bar that changes nothing
	// on the device — and this is the screen where pressing the button is the
	// dangerous part, so the list must hold only real edits.
	sameAsAbsent string
	// shown and shownBefore override the diff's two columns for a key whose
	// stored form is not what the panel says: the address pool is kept as
	// offsets from the network address and shown as addresses (D-44), and
	// "100 → 120" in the apply bar would be the operating system talking.
	shown       string
	shownBefore string
}

// wanSettings turns a requested configuration into the keys to write, and
// refuses anything that would not work before it touches the device.
func wanSettings(cfg core.WANConfig) ([]wanSetting, error) {
	switch cfg.Proto {
	case core.WANProtoDHCP:
		out := []wanSetting{{key: "proto", value: "dhcp", label: labelFor("network", roleUplink, "proto")}}
		if len(cfg.DNS) == 0 {
			// Back to the provider's resolvers: the other direction of the pair
			// below, or the panel can set this and never unset it.
			//
			// Both keys are REMOVED rather than set to their defaults. Absent is
			// what "use the provider's resolvers" looks like on a device that was
			// never touched, so writing `peerdns=1` instead would put a row in
			// the apply bar on every fresh uplink \u2014 asking somebody to confirm a
			// change to nothing, on the one screen where confirming is the
			// dangerous act.
			return append(out,
				wanSetting{
					key:          "peerdns",
					remove:       true,
					sameAsAbsent: "1",
					label:        labelFor("network", roleUplink, "peerdns"),
				},
				wanSetting{key: "dns", remove: true, label: labelFor("network", roleUplink, "dns")},
			), nil
		}
		// Resolvers given alongside DHCP are not a contradiction — wanting the
		// address from the provider and the resolvers from somewhere else is
		// the normal reason people change DNS at all. But netifd would let the
		// provider's resolvers win unless peerdns is turned off, so asking for
		// one without the other silently does nothing.
		out = append(out, wanSetting{
			key: "peerdns", value: "0", label: labelFor("network", roleUplink, "peerdns"),
		})
		return appendDNS(out, cfg.DNS)

	case core.WANProtoStatic:
		if ip := net.ParseIP(cfg.Address); ip == nil || ip.To4() == nil {
			return nil, fmt.Errorf("openwrt: %q is not an IPv4 address", cfg.Address)
		}
		if err := validNetmask(cfg.Netmask); err != nil {
			return nil, err
		}
		if cfg.Gateway != "" && net.ParseIP(cfg.Gateway) == nil {
			return nil, fmt.Errorf("openwrt: %q is not a gateway address", cfg.Gateway)
		}
		out := []wanSetting{
			{key: "proto", value: "static", label: labelFor("network", roleUplink, "proto")},
			{key: "ipaddr", value: cfg.Address, label: labelFor("network", roleUplink, "ipaddr")},
			{key: "netmask", value: cfg.Netmask, label: labelFor("network", roleUplink, "netmask")},
		}
		if cfg.Gateway != "" {
			out = append(out, wanSetting{
				key: "gateway", value: cfg.Gateway, label: labelFor("network", roleUplink, "gateway"),
			})
		}
		return appendDNS(out, cfg.DNS)

	case core.WANProtoPPPoE:
		if strings.TrimSpace(cfg.Username) == "" {
			return nil, fmt.Errorf("openwrt: PPPoE needs a user name")
		}
		out := []wanSetting{
			{key: "proto", value: "pppoe", label: labelFor("network", roleUplink, "proto")},
			{key: "username", value: cfg.Username, label: labelFor("network", roleUplink, "username")},
		}
		if cfg.Password != "" {
			out = append(out, wanSetting{
				key:    "password",
				value:  cfg.Password,
				label:  labelFor("network", roleUplink, "password"),
				secret: true,
			})
		}
		return appendDNS(out, cfg.DNS)

	default:
		return nil, fmt.Errorf("openwrt: unknown connection type %q", cfg.Proto)
	}
}

func appendDNS(out []wanSetting, dns []string) ([]wanSetting, error) {
	if len(dns) == 0 {
		return out, nil
	}
	for _, d := range dns {
		if net.ParseIP(d) == nil {
			return nil, fmt.Errorf("openwrt: %q is not a resolver address", d)
		}
	}
	return append(out, wanSetting{
		key: "dns", value: strings.Join(dns, " "), label: labelFor("network", roleUplink, "dns"),
	}), nil
}

// validNetmask accepts a dotted IPv4 mask that is actually a mask: 255.255.0.1
// parses as an address and is not one, and netifd would take it without
// complaint and build an unreachable network out of it.
func validNetmask(mask string) error {
	ip := net.ParseIP(mask)
	if ip == nil || ip.To4() == nil {
		return fmt.Errorf("openwrt: %q is not a network mask", mask)
	}
	ones, bits := net.IPMask(ip.To4()).Size()
	if bits == 0 || ones == 0 {
		return fmt.Errorf("openwrt: %q is not a contiguous network mask", mask)
	}
	return nil
}

// redact keeps secrets out of the diff. The apply bar is shown on screen and
// copied into support threads; a password has no business in either.
func redact(secret bool, value string) string {
	if !secret {
		return value
	}
	if value == "" {
		return ""
	}
	return "••••"
}

// StagedChanges reads what `uci changes` reports, so the answer comes from the
// device's own staging area rather than from anything this process remembers.
// That matters after a restart: the draft outlives the daemon.
//
// It must describe that draft exactly the way StageWAN described it when it
// was created, because the operator cannot tell the two paths apart: reloading
// the page swaps one for the other. Until M3.1a this one spoke uci — it put
// the raw key in the label and had no "before" value at all.
func (m networkManager) StagedChanges() ([]core.ConfigChange, error) {
	if m.run == nil {
		return nil, core.ErrNotImplemented
	}
	ctx, cancel := context.WithTimeout(context.Background(), stageTimeout)
	defer cancel()

	out, err := m.run(ctx, "uci", "changes")
	if err != nil {
		return nil, fmt.Errorf("openwrt: read staged changes: %w", err)
	}

	edits := parseStagedEdits(string(out))
	if len(edits) == 0 {
		return nil, nil
	}

	// Two reads per configuration file, not two per key: the values as they
	// are on disk, and the values the draft would produce.
	//
	// Neither column is computed here on purpose. A draft can touch the same
	// key more than once — `uci changes` on a live device showed
	// `network.lan.dns='203.0.113.53'` followed by `network.lan.dns+='…54'` —
	// and adding those up by hand produced two rows called "Resolvers", each
	// with half the answer. uci already knows the result; asking it is both
	// shorter and correct.
	before, after := map[string]string{}, map[string]string{}
	for _, config := range configsOf(edits) {
		if values, err := m.committedValues(ctx, config); err == nil {
			for k, v := range values {
				before[k] = v
			}
		} // else: the "before" column is lost, the draft is not — see below.
		if values, err := m.stagedValues(ctx, config); err == nil {
			for k, v := range values {
				after[k] = v
			}
		}
	}

	// Which section is which is a question only the device can answer: the
	// uplink is `wan` on most routers and `lanwan` on one of our own stands,
	// and a reservation is any `dhcp` section whose type is `host`. Guessing
	// wrong would print "Address on the internet side" above a LAN address.
	uplink := ""
	if wan, err := m.WANInfo(); err == nil {
		uplink = wan.Interface.Name
	}

	// One row per key, in the order the device reported them. A key the draft
	// no longer contains was removed, which reads as "→ nothing".
	changes := make([]core.ConfigChange, 0, len(edits))
	seen := map[string]bool{}
	for _, e := range edits {
		if seen[e.key] {
			continue
		}
		seen[e.key] = true

		from, to := before[e.key], after[e.key]
		if from == to {
			// Staged back to what it already was. uci still lists it; showing
			// it would ask the operator to confirm a change to nothing.
			continue
		}
		secret := secretOption(e.option)
		role := roleOf(e.config, e.section, after[e.config+"."+e.section], uplink)
		changes = append(changes, core.ConfigChange{
			Label:     labelFor(e.config, role, e.option),
			From:      redact(secret, from),
			To:        redact(secret, to),
			Dangerous: dangerousConfig(e.config),
			Detail:    e.key,
		})
	}
	return changes, nil
}

// stagedEdit is one line of `uci changes`, taken apart. It says WHICH key the
// draft touches; what the key now holds is asked of uci rather than read off
// the line, because one key can appear on several lines.
type stagedEdit struct {
	key     string // network.wan.proto
	config  string // network
	section string // wan
	option  string // proto
}

// roleOf says what a section is to the panel. sectionType is what `uci show`
// printed for the section's own line, and uplink is the interface the device
// currently routes through \u2014 both are read from the device rather than
// assumed, because neither is derivable from the name.
func roleOf(config, section, sectionType, uplink string) string {
	switch {
	case sectionType == "host":
		return roleHost
	case section == lanSection:
		return roleLAN
	case config == "network" && uplink != "" && section == uplink:
		return roleUplink
	case config == "network" && section == "wan":
		// The device could not be asked \u2014 it is unreachable, or this build has
		// no bus \u2014 and a section literally called `wan` is the uplink by the
		// same OpenWrt convention WANInfo falls back to. Without this the
		// words degrade to "Network setting" exactly when the operator is
		// reading a draft on a device that stopped answering.
		return roleUplink
	default:
		return ""
	}
}

// parseStagedEdits reads `uci changes`. The shapes below were captured from a
// live 23.05 device rather than assumed, and one of them used to be dropped on
// the floor here: a deletion carries no `=`, so splitting on it discarded the
// line and the operator was asked to confirm a draft with a setting silently
// missing from the list.
//
//	network.wan.proto='static'   a value was set
//	network.wan.dns+='192.0.2.1' an item was appended to a list
//	-network.wan.gateway         an option was removed
//	+network.newsec='interface'  a section was added
func parseStagedEdits(out string) []stagedEdit {
	var edits []stagedEdit
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		removal := strings.HasPrefix(line, "-")
		line = strings.TrimPrefix(line, "-")
		line = strings.TrimPrefix(line, "+")

		key, _, hasValue := strings.Cut(line, "=")
		if !hasValue && !removal {
			// Not a shape we know. Guessing at it would put an invented row
			// in front of somebody about to press a button.
			continue
		}
		key = strings.TrimSuffix(key, "+")

		e := stagedEdit{key: key}
		parts := strings.Split(key, ".")
		if len(parts) < 2 {
			continue
		}
		e.config = parts[0]
		e.section = parts[1]
		if len(parts) >= 3 {
			e.option = parts[len(parts)-1]
		}
		edits = append(edits, e)
	}
	return edits
}

// configsOf lists the configuration files a draft touches, once each and in a
// stable order.
func configsOf(edits []stagedEdit) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range edits {
		if e.config == "" || seen[e.config] {
			continue
		}
		seen[e.config] = true
		out = append(out, e.config)
	}
	return out
}

// unquoteUCI turns a uci-printed value into one the panel can show. A list is
// printed as `'a' 'b'` and is shown as one line, because that is how the panel
// asks for it too (resolvers are one field).
func unquoteUCI(v string) string {
	v = strings.TrimSpace(v)
	if !strings.Contains(v, "'") {
		return v
	}
	parts := strings.Split(v, "'")
	var items []string
	// Quoted items sit at the odd indexes of a split on the quote character.
	for i := 1; i < len(parts); i += 2 {
		if parts[i] != "" {
			items = append(items, parts[i])
		}
	}
	return strings.Join(items, " ")
}

// stagedValues returns the values of one configuration WITH the draft applied
// — the "after" column. This is the ordinary uci view, and the reason it is a
// separate read is that the draft is the thing being described: no arithmetic
// over `uci changes` lines can beat asking uci what the result is.
func (m networkManager) stagedValues(ctx context.Context, config string) (map[string]string, error) {
	if !sectionNameRe.MatchString(config) {
		return nil, fmt.Errorf("openwrt: %q is not a configuration name", config)
	}
	out, err := m.run(ctx, "uci", "-q", "show", config)
	if err != nil {
		return nil, fmt.Errorf("openwrt: read staged %s: %w", config, err)
	}
	return parseUCIShow(config, string(out)), nil
}

// committedPrefix starts the package name a configuration is read under when
// the staged draft has to be kept out of the answer. The real name carries the
// configuration too (`veilbridge_committed_network`), so a call is readable in
// a process list and in a test's recorded arguments.
const committedPrefix = "veilbridge_committed_"

// committedValues returns the values of one configuration as they are on disk,
// i.e. WITHOUT the staged draft. It is what fills the "before" column of the
// apply bar for a draft this process did not stage itself.
//
// uci has no flag for this, which was measured and not assumed: on a 23.05
// device both `-t <empty dir>` and `-P <empty dir>` still returned the staged
// value, because /tmp/.uci stays in the search path and nothing removes it.
// What uci does have is a search path for config FILES plus a draft keyed by
// PACKAGE NAME — so the same file, reachable under a different package name,
// is read with no draft applied.
//
// The file is reached by a symlink rather than a copy on purpose: a copy of
// /etc/config/network would put the PPPoE password in a second place on disk.
// The link is created here in Go, so the adapter's allow-list of programs
// stays exactly as short as it was.
func (m networkManager) committedValues(ctx context.Context, config string) (map[string]string, error) {
	if !sectionNameRe.MatchString(config) {
		return nil, fmt.Errorf("openwrt: %q is not a configuration name", config)
	}
	dir := m.configDir
	if dir == "" {
		dir = "/etc/config"
	}

	linkDir, err := os.MkdirTemp("", "veilbridge-committed-")
	if err != nil {
		return nil, fmt.Errorf("openwrt: read committed %s: %w", config, err)
	}
	defer func() { _ = os.RemoveAll(linkDir) }()

	alias := committedPrefix + config
	if err := os.Symlink(filepath.Join(dir, config), filepath.Join(linkDir, alias)); err != nil {
		return nil, fmt.Errorf("openwrt: read committed %s: %w", config, err)
	}

	out, err := m.run(ctx, "uci", "-q", "-c", linkDir, "show", alias)
	if err != nil {
		return nil, fmt.Errorf("openwrt: read committed %s: %w", config, err)
	}
	return parseUCIShow(config, string(out)), nil
}

// parseUCIShow reads `uci show` output and re-addresses it to the real
// configuration name, so lookups use the keys `uci changes` speaks — the
// committed read answers under a different package name by design.
func parseUCIShow(config, out string) map[string]string {
	values := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		// A line like `veilbridge_committed_network.wan=interface` names a
		// section rather than a setting. It is kept, not skipped: a draft that
		// adds or removes a whole section has nothing else to show, and
		// dropping these made "this connection will be deleted" an empty row.
		_, rest, ok := strings.Cut(key, ".")
		if !ok {
			continue
		}
		values[config+"."+rest] = unquoteUCI(value)
	}
	return values
}

// DiscardStaged throws the draft away. It reverts the staging area only: the
// live configuration is not touched, which is why this is safe to call from a
// failed StageWAN.
func (m networkManager) DiscardStaged() error {
	if m.run == nil {
		return core.ErrNotImplemented
	}
	ctx, cancel := context.WithTimeout(context.Background(), stageTimeout)
	defer cancel()
	// Both files this package writes: since M3.2 a draft can hold the local
	// network and its address handout as well, and discarding half of it
	// would leave the operator with a pending change they thought they threw
	// away \u2014 which the next apply would then commit.
	for _, config := range []string{"network", "dhcp"} {
		if err := m.discardConfig(ctx, config); err != nil {
			return err
		}
	}
	return nil
}

// discardConfig throws away the draft of one configuration file. It reverts
// the staging area only: the live configuration is untouched, which is why it
// is safe to call from a failed stage.
func (m networkManager) discardConfig(ctx context.Context, config string) error {
	if !sectionNameRe.MatchString(config) {
		return fmt.Errorf("openwrt: %q is not a configuration name", config)
	}
	if _, err := m.run(ctx, "uci", "revert", config); err != nil {
		return fmt.Errorf("openwrt: discard staged %s changes: %w", config, err)
	}
	return nil
}

func (m networkManager) uciGet(ctx context.Context, key string) string {
	out, err := m.run(ctx, "uci", "-q", "get", key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// uciDelete stages the removal of a key. Like uciSet it only writes to the
// staging area; the apply transaction is still the only thing that commits.
func (m networkManager) uciDelete(ctx context.Context, key string) error {
	if _, err := m.run(ctx, "uci", "delete", key); err != nil {
		return fmt.Errorf("openwrt: stage removal of %s: %w", key, err)
	}
	return nil
}

func (m networkManager) uciSet(ctx context.Context, key, value string) error {
	// key and value are passed as separate arguments and never through a
	// shell; the runner's allow-list decides what may be executed at all.
	if _, err := m.run(ctx, "uci", "set", key+"="+value); err != nil {
		return fmt.Errorf("openwrt: stage %s: %w", key, err)
	}
	return nil
}

var _ core.NetworkWriter = networkManager{}
