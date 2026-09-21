package openwrt

import (
	"context"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/veilbridge-os/veilbridge/internal/core"
)

// Reading the local network (M3.2).
//
// Three sources, because the device keeps the answer in three places: netifd
// knows the interface, uci knows the handout, and the DHCP server keeps its
// leases in a file. They are read together and returned as one answer — see
// core.LANStatus for why.
//
// Two facts here were measured on both branches rather than assumed:
//
//   - `ubus call dhcp ipv4leases` exists on 23.05 and is GONE on 25.12, which
//     only offers ipv6leases. The lease FILE is the one source both branches
//     agree on, so that is what this reads.
//   - the file's shape is `<expiry> <mac> <ip> <name|*> <client-id>`, captured
//     from a real client on the reference router:
//     `1790012670 1a:a6:05:03:d4:9c 192.168.1.222 stend-noutbuk 01:1a:...`
//     A client that gave no name writes `*` there, not an empty field.
const lanTimeout = 15 * time.Second

// lanSection is the interface the panel calls "the local network". Picking it
// by name is the same convention OpenWrt itself uses, and the same one WANInfo
// falls back to; unlike the uplink there is no second signal (a default route)
// to check it against, so this is a convention and is written down as one.
const lanSection = "lan"

// defaultLeaseFile is where dnsmasq keeps leases unless told otherwise. It is
// a fallback only: the configured path wins, because a device that moved the
// file would otherwise show "no clients" forever.
const defaultLeaseFile = "/tmp/dhcp.leases"

// LANInfo reads the local network, its address handout and its clients.
func (m networkManager) LANInfo() (core.LANStatus, error) {
	ifaces, err := m.Interfaces()
	if err != nil {
		return core.LANStatus{}, err
	}
	var lan core.NetworkInterface
	for _, i := range ifaces {
		if i.Name == lanSection {
			lan = i
			break
		}
	}
	if lan.Name == "" {
		// A box with a single network card has no local network to manage.
		// That is a fact about the hardware, not a failure (D-20).
		return core.LANStatus{}, core.ErrNoLAN
	}

	out := core.LANStatus{Interface: lan}
	if m.run == nil {
		// No command runner: the interface is all this build can answer.
		return out, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), lanTimeout)
	defer cancel()

	out.Handout = m.handout(ctx, lan)
	out.Reserved = m.reserved(ctx)
	out.Leases = m.leases(ctx)
	return out, nil
}

// handout turns the stored offsets into the addresses the panel shows.
func (m networkManager) handout(ctx context.Context, lan core.NetworkInterface) core.AddressHandout {
	// `ignore=1` is how OpenWrt switches the handout off for an interface.
	if m.uciGet(ctx, "dhcp."+lanSection+".ignore") == "1" {
		return core.AddressHandout{}
	}
	start, startErr := strconv.Atoi(m.uciGet(ctx, "dhcp."+lanSection+".start"))
	limit, limitErr := strconv.Atoi(m.uciGet(ctx, "dhcp."+lanSection+".limit"))
	if startErr != nil || limitErr != nil || limit <= 0 {
		// A section that exists but carries no pool hands out nothing we can
		// describe; saying so beats inventing a range.
		return core.AddressHandout{}
	}

	h := core.AddressHandout{
		Enabled:      true,
		LeaseSeconds: parseLeaseTime(m.uciGet(ctx, "dhcp."+lanSection+".leasetime")),
	}
	if first, last, ok := poolRange(lan.IPv4, start, limit); ok {
		h.First, h.Last = first, last
	}
	return h
}

// poolRange computes the ends of the pool from the interface's own address.
// The offsets are counted from the network address, so the answer depends on
// the mask as much as on the numbers — which is exactly why the panel must not
// print the numbers themselves.
func poolRange(addresses []string, start, limit int) (string, string, bool) {
	for _, cidr := range addresses {
		ip, network, err := net.ParseCIDR(cidr)
		if err != nil || ip.To4() == nil {
			continue
		}
		base := network.IP.To4()
		if base == nil {
			continue
		}
		first := addOffset(base, start)
		last := addOffset(base, start+limit-1)
		if first == nil || last == nil || !network.Contains(first) || !network.Contains(last) {
			// A pool that runs past the end of its own network is a
			// misconfiguration on the device; reporting no range at all is
			// honest, inventing a clamped one is not.
			return "", "", false
		}
		return first.String(), last.String(), true
	}
	return "", "", false
}

func addOffset(base net.IP, offset int) net.IP {
	if offset < 0 {
		return nil
	}
	v := uint32(base[0])<<24 | uint32(base[1])<<16 | uint32(base[2])<<8 | uint32(base[3])
	sum := uint64(v) + uint64(offset)
	if sum > 0xFFFFFFFF {
		return nil
	}
	n := uint32(sum)
	return net.IPv4(byte(n>>24), byte(n>>16), byte(n>>8), byte(n)).To4()
}

// parseLeaseTime reads the durations OpenWrt accepts: `12h`, `30m`, `infinite`
// and a bare number of seconds.
func parseLeaseTime(v string) int64 {
	v = strings.TrimSpace(v)
	if v == "" || v == "infinite" {
		return 0
	}
	unit := int64(1)
	switch v[len(v)-1] {
	case 'h':
		unit, v = 3600, v[:len(v)-1]
	case 'm':
		unit, v = 60, v[:len(v)-1]
	case 's':
		unit, v = 1, v[:len(v)-1]
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n * unit
}

// reserved lists the addresses pinned to a hardware address.
func (m networkManager) reserved(ctx context.Context) []core.ReservedAddress {
	out, err := m.run(ctx, "uci", "-q", "show", "dhcp")
	if err != nil {
		return nil
	}
	// `uci show` prints one line per option; the section id is what a later
	// delete has to address, so it is carried through as the entry's own id.
	byID := map[string]*core.ReservedAddress{}
	var order []string
	for _, line := range strings.Split(string(out), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		parts := strings.Split(key, ".")
		if len(parts) == 2 && unquoteUCI(value) == "host" {
			if _, seen := byID[parts[1]]; !seen {
				byID[parts[1]] = &core.ReservedAddress{ID: parts[1]}
				order = append(order, parts[1])
			}
			continue
		}
		if len(parts) != 3 {
			continue
		}
		entry, ok := byID[parts[1]]
		if !ok {
			continue
		}
		switch parts[2] {
		case "mac":
			entry.MAC = unquoteUCI(value)
		case "ip":
			entry.IP = unquoteUCI(value)
		case "name":
			entry.Name = unquoteUCI(value)
		}
	}
	var list []core.ReservedAddress
	for _, id := range order {
		// A host section without both halves reserves nothing.
		if e := byID[id]; e.MAC != "" && e.IP != "" {
			list = append(list, *e)
		}
	}
	return list
}

// leases reads the DHCP server's lease file. The path comes from the device's
// own configuration: hardcoding /tmp/dhcp.leases would show "no clients"
// forever on a device that moved it.
func (m networkManager) leases(ctx context.Context) []core.AddressLease {
	path := m.uciGet(ctx, "dhcp.@dnsmasq[0].leasefile")
	if path == "" {
		path = defaultLeaseFile
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return parseLeases(string(data), time.Now())
}

// parseLeases reads the dnsmasq lease file. Shape captured from a live device:
//
//	1790012670 1a:a6:05:03:d4:9c 192.168.1.222 stend-noutbuk 01:1a:a6:05:03:d4:9c
//	<expiry unix> <mac> <ip> <name or *> <client id>
func parseLeases(data string, now time.Time) []core.AddressLease {
	var out []core.AddressLease
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		expiry, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			continue
		}
		l := core.AddressLease{MAC: fields[1], IP: fields[2]}
		if fields[3] != "*" {
			l.Hostname = fields[3]
		}
		if left := expiry - now.Unix(); left > 0 {
			l.ExpiresSec = left
		}
		out = append(out, l)
	}
	return out
}
