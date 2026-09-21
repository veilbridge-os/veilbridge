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

// Writing the local network (M3.2).
//
// Same rules as the uplink writer next door: everything here stages and
// nothing commits, and a value that does not parse never reaches the device.
// What is different is who gets hurt by a mistake. A bad uplink cuts a remote
// operator off from the internet and comes back by itself; a bad local
// network cuts the person sitting next to the router, on the very network
// being renumbered — their own address stops matching it the instant it
// applies, so the watchdog is the only way back for them too.
//
// The pool is the other difference. The device stores offsets from the
// network address (`start=100`, `limit=150`); the panel speaks addresses
// (D-44). The conversion lives here, in both directions, and the diff shows
// addresses even though the keys carry numbers — otherwise the apply bar
// would ask somebody to confirm "100 → 120".

// hostNameRe is what may be used as a device name in a reservation. dnsmasq
// puts this name into DNS, so anything outside a hostname is refused here
// rather than silently breaking name resolution on the whole network.
var hostNameRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)

// StageLAN stages this router's own address on the local network.
func (m networkManager) StageLAN(cfg core.LANConfig) ([]core.ConfigChange, error) {
	if m.run == nil {
		return nil, core.ErrNotImplemented
	}
	if ip := net.ParseIP(cfg.Address); ip == nil || ip.To4() == nil {
		return nil, fmt.Errorf("openwrt: %q is not an IPv4 address", cfg.Address)
	}
	if err := validNetmask(cfg.Netmask); err != nil {
		return nil, err
	}
	// A router that hands out addresses it cannot reach is a network that
	// half works, which is harder to diagnose than one that plainly does not.
	// So the pool is checked against the new address before it is accepted.
	ctx, cancel := context.WithTimeout(context.Background(), stageTimeout)
	defer cancel()
	if err := m.poolFitsAddress(ctx, cfg.Address, cfg.Netmask); err != nil {
		return nil, err
	}

	return m.stage(ctx, "network", lanSection, roleLAN, []wanSetting{
		{key: "proto", value: "static", label: labelFor("network", roleLAN, "proto")},
		{key: "ipaddr", value: cfg.Address, label: labelFor("network", roleLAN, "ipaddr")},
		{key: "netmask", value: cfg.Netmask, label: labelFor("network", roleLAN, "netmask")},
	})
}

// poolFitsAddress refuses an address that would leave the existing handout
// outside its own network.
func (m networkManager) poolFitsAddress(ctx context.Context, address, netmask string) error {
	start, errStart := strconv.Atoi(m.uciGet(ctx, "dhcp."+lanSection+".start"))
	limit, errLimit := strconv.Atoi(m.uciGet(ctx, "dhcp."+lanSection+".limit"))
	if errStart != nil || errLimit != nil || limit <= 0 {
		return nil // no pool to contradict
	}
	ones, _ := net.IPMask(net.ParseIP(netmask).To4()).Size()
	cidr := fmt.Sprintf("%s/%d", address, ones)
	if _, _, ok := poolRange([]string{cidr}, start, limit); !ok {
		return fmt.Errorf(
			"openwrt: the addresses handed out would fall outside %s/%d", address, ones)
	}
	return nil
}

// StageHandout stages the address handout, converting the addresses the panel
// speaks into the offsets the device stores.
func (m networkManager) StageHandout(cfg core.HandoutConfig) ([]core.ConfigChange, error) {
	if m.run == nil {
		return nil, core.ErrNotImplemented
	}
	ctx, cancel := context.WithTimeout(context.Background(), stageTimeout)
	defer cancel()

	if !cfg.Enabled {
		// Off is `ignore=1`, and the pool keeps its numbers: turning the
		// handout back on must not require typing the range again.
		return m.stage(ctx, "dhcp", lanSection, roleLAN, []wanSetting{
			{key: "ignore", value: "1", label: labelFor("dhcp", roleLAN, "ignore")},
		})
	}

	network, err := m.lanNetwork(ctx)
	if err != nil {
		return nil, err
	}
	start, limit, err := poolOffsets(network, cfg.First, cfg.Last)
	if err != nil {
		return nil, err
	}

	sets := []wanSetting{
		{key: "ignore", value: "", remove: true, label: labelFor("dhcp", roleLAN, "ignore")},
		{
			key: "start", value: strconv.Itoa(start),
			label: labelFor("dhcp", roleLAN, "start"),
			// The diff speaks addresses even though the key carries a number:
			// "100 → 120" is the operating system talking (D-3, D-44).
			shown: cfg.First,
			shownBefore: offsetAsAddress(
				network, m.uciGet(ctx, "dhcp."+lanSection+".start")),
		},
		{
			key: "limit", value: strconv.Itoa(limit),
			label: labelFor("dhcp", roleLAN, "limit"),
			shown: cfg.Last,
			shownBefore: lastAsAddress(network,
				m.uciGet(ctx, "dhcp."+lanSection+".start"),
				m.uciGet(ctx, "dhcp."+lanSection+".limit")),
		},
	}
	if cfg.LeaseSeconds > 0 {
		sets = append(sets, wanSetting{
			key:   "leasetime",
			value: formatLeaseTime(cfg.LeaseSeconds),
			label: labelFor("dhcp", roleLAN, "leasetime"),
		})
	}
	return m.stage(ctx, "dhcp", lanSection, roleLAN, sets)
}

// lanNetwork returns the network the local address sits in, which is what the
// pool offsets are counted from.
func (m networkManager) lanNetwork(ctx context.Context) (*net.IPNet, error) {
	_ = ctx
	lan, err := m.LANInfo()
	if err != nil {
		return nil, err
	}
	for _, cidr := range lan.Interface.IPv4 {
		if _, network, err := net.ParseCIDR(cidr); err == nil && network.IP.To4() != nil {
			return network, nil
		}
	}
	return nil, fmt.Errorf("openwrt: the local network has no IPv4 address to count from")
}

// poolOffsets turns first/last addresses into the stored offset and count,
// refusing anything that would not work before the device sees it.
func poolOffsets(network *net.IPNet, first, last string) (int, int, error) {
	f, l := net.ParseIP(first), net.ParseIP(last)
	if f == nil || f.To4() == nil {
		return 0, 0, fmt.Errorf("openwrt: %q is not an IPv4 address", first)
	}
	if l == nil || l.To4() == nil {
		return 0, 0, fmt.Errorf("openwrt: %q is not an IPv4 address", last)
	}
	if !network.Contains(f) || !network.Contains(l) {
		return 0, 0, fmt.Errorf(
			"openwrt: %s-%s is outside the local network %s", first, last, network)
	}
	base := ipToUint(network.IP.To4())
	fu, lu := ipToUint(f.To4()), ipToUint(l.To4())
	// Compared before subtracting: these are unsigned, so `last - first` on a
	// reversed pair wraps to an enormous positive number instead of going
	// negative, and the range would be accepted. Found by the test that fed
	// it the ends the wrong way round.
	if lu < fu {
		return 0, 0, fmt.Errorf("openwrt: %s comes before %s", last, first)
	}
	start := int(fu - base)
	count := int(lu-fu) + 1
	if start == 0 {
		// Offset zero is the network address itself, which no client may be
		// given; dnsmasq would take the number and hand out a broken lease.
		return 0, 0, fmt.Errorf("openwrt: %s is the address of the network itself", first)
	}
	return start, count, nil
}

func ipToUint(ip net.IP) uint32 {
	return uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])
}

// offsetAsAddress renders a stored offset for the diff's "before" column.
func offsetAsAddress(network *net.IPNet, stored string) string {
	n, err := strconv.Atoi(strings.TrimSpace(stored))
	if err != nil {
		return ""
	}
	if ip := addOffset(network.IP.To4(), n); ip != nil {
		return ip.String()
	}
	return ""
}

func lastAsAddress(network *net.IPNet, storedStart, storedLimit string) string {
	s, errS := strconv.Atoi(strings.TrimSpace(storedStart))
	l, errL := strconv.Atoi(strings.TrimSpace(storedLimit))
	if errS != nil || errL != nil || l <= 0 {
		return ""
	}
	if ip := addOffset(network.IP.To4(), s+l-1); ip != nil {
		return ip.String()
	}
	return ""
}

// formatLeaseTime writes a duration the way OpenWrt reads it back.
func formatLeaseTime(sec int64) string {
	switch {
	case sec%3600 == 0:
		return strconv.FormatInt(sec/3600, 10) + "h"
	case sec%60 == 0:
		return strconv.FormatInt(sec/60, 10) + "m"
	default:
		return strconv.FormatInt(sec, 10)
	}
}

// StageReservation pins an address to a device. An existing reservation for
// the same hardware address is updated rather than duplicated: two entries for
// one device is a configuration whose behaviour depends on file order.
func (m networkManager) StageReservation(cfg core.ReservationConfig) ([]core.ConfigChange, error) {
	if m.run == nil {
		return nil, core.ErrNotImplemented
	}
	mac, err := net.ParseMAC(strings.TrimSpace(cfg.MAC))
	if err != nil {
		return nil, fmt.Errorf("openwrt: %q is not a hardware address", cfg.MAC)
	}
	ip := net.ParseIP(strings.TrimSpace(cfg.IP))
	if ip == nil || ip.To4() == nil {
		return nil, fmt.Errorf("openwrt: %q is not an IPv4 address", cfg.IP)
	}
	if cfg.Name != "" && !hostNameRe.MatchString(cfg.Name) {
		return nil, fmt.Errorf("openwrt: %q is not a device name", cfg.Name)
	}

	ctx, cancel := context.WithTimeout(context.Background(), stageTimeout)
	defer cancel()

	// The reserved address has to be on the local network, or the device will
	// hand out an address nobody can use and the client will look "connected
	// but silent" — the hardest kind of fault to find.
	if network, err := m.lanNetwork(ctx); err == nil && !network.Contains(ip) {
		return nil, fmt.Errorf("openwrt: %s is outside the local network %s", cfg.IP, network)
	}

	section := ""
	for _, r := range m.reserved(ctx) {
		if strings.EqualFold(r.MAC, mac.String()) {
			section = r.ID
			break
		}
	}
	if section == "" {
		out, err := m.run(ctx, "uci", "add", "dhcp", "host")
		if err != nil {
			return nil, fmt.Errorf("openwrt: stage a reservation: %w", err)
		}
		section = strings.TrimSpace(string(out))
		if !sectionNameRe.MatchString(section) {
			_ = m.discardConfig(ctx, "dhcp")
			return nil, fmt.Errorf("openwrt: the device named the new entry %q", section)
		}
	}

	sets := []wanSetting{
		{key: "mac", value: mac.String(), label: labelFor("dhcp", roleHost, "mac")},
		{key: "ip", value: ip.String(), label: labelFor("dhcp", roleHost, "ip")},
	}
	if cfg.Name != "" {
		sets = append(sets, wanSetting{
			key: "name", value: cfg.Name, label: labelFor("dhcp", roleHost, "name"),
		})
	}
	return m.stage(ctx, "dhcp", section, roleHost, sets)
}

// RemoveReservation stages the removal of one reservation.
func (m networkManager) RemoveReservation(id string) ([]core.ConfigChange, error) {
	if m.run == nil {
		return nil, core.ErrNotImplemented
	}
	if !sectionNameRe.MatchString(id) {
		return nil, fmt.Errorf("openwrt: %q is not an entry on this device", id)
	}
	ctx, cancel := context.WithTimeout(context.Background(), stageTimeout)
	defer cancel()

	var target *core.ReservedAddress
	for _, r := range m.reserved(ctx) {
		if r.ID == id {
			found := r
			target = &found
			break
		}
	}
	if target == nil {
		// Refusing beats deleting whatever else happens to carry that id: the
		// draft is addressed by section, and sections are reused after a
		// commit.
		return nil, fmt.Errorf("openwrt: no reservation %q on this device", id)
	}
	if _, err := m.run(ctx, "uci", "delete", "dhcp."+id); err != nil {
		return nil, fmt.Errorf("openwrt: stage removal of a reservation: %w", err)
	}
	return []core.ConfigChange{{
		Label:     labelFor("dhcp", roleHost, ""),
		From:      reservationWords(*target),
		To:        "",
		Dangerous: dangerousConfig("dhcp"),
		Detail:    "dhcp." + id,
	}}, nil
}

// reservationWords describes a reservation for the diff, in one line a person
// can check against the sticker on the device.
func reservationWords(r core.ReservedAddress) string {
	if r.Name != "" {
		return fmt.Sprintf("%s (%s) %s", r.Name, r.MAC, r.IP)
	}
	return fmt.Sprintf("%s %s", r.MAC, r.IP)
}
