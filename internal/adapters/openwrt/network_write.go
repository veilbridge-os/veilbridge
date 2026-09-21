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
// Keyed by `<config>.<option>`, not by section: today only the uplink is
// editable, so "address" means the address on the internet side. When M3.2
// makes the local network editable, these words need a section role — the
// same option name will mean something else there.
var optionLabels = map[string]string{
	"network.proto":    "Internet connection type",
	"network.ipaddr":   "Address on the internet side",
	"network.netmask":  "Network mask",
	"network.gateway":  "Gateway",
	"network.dns":      "Resolvers",
	"network.peerdns":  "Use the provider's resolvers",
	"network.username": "Provider login",
	"network.password": "Provider password",
}

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

// labelFor turns a configuration key into what the panel calls it.
func labelFor(config, option string) string {
	if option == "" {
		if l, ok := sectionLabels[config]; ok {
			return l
		}
		return "Configuration section"
	}
	if l, ok := optionLabels[config+"."+option]; ok {
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

	// The "before" values are read first, so the diff describes this device
	// and not an assumption about it. A key that does not exist yet reads as
	// empty, which is exactly how it should appear in the diff.
	changes := make([]core.ConfigChange, 0, len(sets))
	for _, s := range sets {
		key := fmt.Sprintf("network.%s.%s", iface, s.key)
		before := m.uciGet(ctx, key)
		if before == s.value {
			continue
		}
		if err := m.uciSet(ctx, key, s.value); err != nil {
			// A half-staged batch is not left behind: the draft is dropped so
			// the operator never confirms a change they did not see in full.
			_ = m.DiscardStaged()
			return nil, err
		}
		changes = append(changes, core.ConfigChange{
			Label:     s.label,
			From:      redact(s.secret, before),
			To:        redact(s.secret, s.value),
			Dangerous: true, // every uplink edit can cut our own access
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
}

// wanSettings turns a requested configuration into the keys to write, and
// refuses anything that would not work before it touches the device.
func wanSettings(cfg core.WANConfig) ([]wanSetting, error) {
	switch cfg.Proto {
	case core.WANProtoDHCP:
		out := []wanSetting{{key: "proto", value: "dhcp", label: labelFor("network", "proto")}}
		if len(cfg.DNS) == 0 {
			return out, nil
		}
		// Resolvers given alongside DHCP are not a contradiction — wanting the
		// address from the provider and the resolvers from somewhere else is
		// the normal reason people change DNS at all. But netifd would let the
		// provider's resolvers win unless peerdns is turned off, so asking for
		// one without the other silently does nothing.
		out = append(out, wanSetting{
			key: "peerdns", value: "0", label: labelFor("network", "peerdns"),
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
			{key: "proto", value: "static", label: labelFor("network", "proto")},
			{key: "ipaddr", value: cfg.Address, label: labelFor("network", "ipaddr")},
			{key: "netmask", value: cfg.Netmask, label: labelFor("network", "netmask")},
		}
		if cfg.Gateway != "" {
			out = append(out, wanSetting{
				key: "gateway", value: cfg.Gateway, label: labelFor("network", "gateway"),
			})
		}
		return appendDNS(out, cfg.DNS)

	case core.WANProtoPPPoE:
		if strings.TrimSpace(cfg.Username) == "" {
			return nil, fmt.Errorf("openwrt: PPPoE needs a user name")
		}
		out := []wanSetting{
			{key: "proto", value: "pppoe", label: labelFor("network", "proto")},
			{key: "username", value: cfg.Username, label: labelFor("network", "username")},
		}
		if cfg.Password != "" {
			out = append(out, wanSetting{
				key:    "password",
				value:  cfg.Password,
				label:  labelFor("network", "password"),
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
		key: "dns", value: strings.Join(dns, " "), label: labelFor("network", "dns"),
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
		changes = append(changes, core.ConfigChange{
			Label:     labelFor(e.config, e.option),
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
	key    string // network.wan.proto
	config string // network
	option string // proto
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
	if _, err := m.run(ctx, "uci", "revert", "network"); err != nil {
		return fmt.Errorf("openwrt: discard staged network changes: %w", err)
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

func (m networkManager) uciSet(ctx context.Context, key, value string) error {
	// key and value are passed as separate arguments and never through a
	// shell; the runner's allow-list decides what may be executed at all.
	if _, err := m.run(ctx, "uci", "set", key+"="+value); err != nil {
		return fmt.Errorf("openwrt: stage %s: %w", key, err)
	}
	return nil
}

var _ core.NetworkWriter = networkManager{}
