package openwrt

import (
	"context"
	"fmt"
	"net"
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
		out := []wanSetting{{key: "proto", value: "dhcp", label: "Internet connection type"}}
		if len(cfg.DNS) == 0 {
			return out, nil
		}
		// Resolvers given alongside DHCP are not a contradiction — wanting the
		// address from the provider and the resolvers from somewhere else is
		// the normal reason people change DNS at all. But netifd would let the
		// provider's resolvers win unless peerdns is turned off, so asking for
		// one without the other silently does nothing.
		out = append(out, wanSetting{
			key: "peerdns", value: "0", label: "Use the provider's resolvers",
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
			{key: "proto", value: "static", label: "Internet connection type"},
			{key: "ipaddr", value: cfg.Address, label: "Address on the internet side"},
			{key: "netmask", value: cfg.Netmask, label: "Network mask"},
		}
		if cfg.Gateway != "" {
			out = append(out, wanSetting{key: "gateway", value: cfg.Gateway, label: "Gateway"})
		}
		return appendDNS(out, cfg.DNS)

	case core.WANProtoPPPoE:
		if strings.TrimSpace(cfg.Username) == "" {
			return nil, fmt.Errorf("openwrt: PPPoE needs a user name")
		}
		out := []wanSetting{
			{key: "proto", value: "pppoe", label: "Internet connection type"},
			{key: "username", value: cfg.Username, label: "Provider login"},
		}
		if cfg.Password != "" {
			out = append(out, wanSetting{
				key: "password", value: cfg.Password, label: "Provider password", secret: true,
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
		key: "dns", value: strings.Join(dns, " "), label: "Resolvers",
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
	var changes []core.ConfigChange
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Lines look like `network.wan.proto='static'` (and `-network.foo` for
		// a deletion). Anything else is not ours to interpret.
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimPrefix(key, "+")
		secret := strings.HasSuffix(key, ".password") || strings.HasSuffix(key, ".key")
		changes = append(changes, core.ConfigChange{
			Label:     key,
			To:        redact(secret, strings.Trim(value, "'")),
			Dangerous: strings.HasPrefix(key, "network.") || strings.HasPrefix(key, "firewall."),
			Detail:    key,
		})
	}
	return changes, nil
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
