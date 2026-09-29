package openwrt

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Traffic per device (#56, D-92, D-99).
//
// The router counts in a table of the panel's own, not in fw4's: fw4 rewrites
// its table on every reload, while this one survives `fw4 reload` and a
// firewall restart with its counters (measured on 25.12.5, 29.09). It lives in
// the kernel's memory only, so a reboot starts it over, which is what the
// screen says.
//
// Sent is counted by the sender's hardware address. Received cannot be: in
// the forward hook the next hop's hardware address is not known yet, so it is
// counted by the destination address and handed to a device by the addresses
// the router knows it by (leases, neighbours). IPv6 privacy addresses change,
// so an address keeps its owner in memory after it is gone from the tables.

const (
	trafficTable = "veilbridge_traffic"
	nftProgram   = "/usr/sbin/nft"
	// bootWindow: counting that started this soon after boot started "with the
	// router": the daemon comes up a little after the kernel.
	bootWindow = 300
	// maxAddrOwners bounds the address → device memory like maxRemembered.
	maxAddrOwners = 8192
)

// trafficRuleset is the whole table, loaded atomically with `nft -f`.
func trafficRuleset(lanDev string, uptime int64) string {
	return fmt.Sprintf(`table inet %[1]s {
	comment "vb uptime=%[3]d dev=%[2]s"
	set up { type ether_addr; flags dynamic; counter; size 4096; }
	set down4 { type ipv4_addr; flags dynamic; counter; size 8192; }
	set down6 { type ipv6_addr; flags dynamic; counter; size 8192; }
	chain tally {
		type filter hook forward priority filter + 10; policy accept;
		iifname "%[2]s" update @up { ether saddr }
		oifname "%[2]s" meta nfproto ipv4 update @down4 { ip daddr }
		oifname "%[2]s" meta nfproto ipv6 update @down6 { ip6 daddr }
	}
}
`, trafficTable, lanDev, uptime)
}

// trafficRead is one reading of the table.
type trafficRead struct {
	up        map[string]int64 // hardware address → bytes sent
	down      map[string]int64 // address → bytes received
	startedAt int64            // uptime when counting started
	dev       string           // the local network device the rules name
}

// parseTraffic reads `nft -j list table inet veilbridge_traffic`.
func parseTraffic(body []byte) (trafficRead, bool) {
	var doc struct {
		Nftables []struct {
			Table *struct {
				Name    string `json:"name"`
				Comment string `json:"comment"`
			} `json:"table"`
			Set *struct {
				Name string `json:"name"`
				Elem []struct {
					Elem struct {
						Val     json.RawMessage `json:"val"`
						Counter struct {
							Bytes int64 `json:"bytes"`
						} `json:"counter"`
					} `json:"elem"`
				} `json:"elem"`
			} `json:"set"`
		} `json:"nftables"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return trafficRead{}, false
	}
	r := trafficRead{up: map[string]int64{}, down: map[string]int64{}, startedAt: -1}
	sets := 0
	for _, o := range doc.Nftables {
		if t := o.Table; t != nil && t.Name == trafficTable {
			for _, f := range strings.Fields(t.Comment) {
				if v, ok := strings.CutPrefix(f, "uptime="); ok {
					if n, err := strconv.ParseInt(v, 10, 64); err == nil {
						r.startedAt = n
					}
				}
				if v, ok := strings.CutPrefix(f, "dev="); ok {
					r.dev = v
				}
			}
		}
		s := o.Set
		if s == nil {
			continue
		}
		switch s.Name {
		case "up", "down4", "down6":
			sets++
		default:
			continue
		}
		for _, e := range s.Elem {
			var key string
			if json.Unmarshal(e.Elem.Val, &key) != nil {
				continue
			}
			if s.Name == "up" {
				if mac, err := net.ParseMAC(key); err == nil {
					r.up[mac.String()] += e.Elem.Counter.Bytes
				}
				continue
			}
			if ip := net.ParseIP(key); ip != nil {
				r.down[ip.String()] += e.Elem.Counter.Bytes
			}
		}
	}
	return r, sets == 3 && r.startedAt >= 0
}

// uptimeSec reads /proc/uptime: monotonic, unlike the clock (D-89).
func (m *deviceManager) uptimeSec() (int64, bool) {
	b, err := m.sys.ReadFile("/proc/uptime")
	if err != nil {
		return 0, false
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(f[0], 64)
	return int64(v), err == nil
}

// traffic reads the counters, putting the table in place first if it is not
// there or names another device. ok is false when the router cannot count.
func (m *deviceManager) traffic(ctx context.Context, lanDev string) (trafficRead, int64, bool) {
	if m.net.run == nil {
		return trafficRead{}, 0, false
	}
	now, ok := m.uptimeSec()
	if !ok {
		return trafficRead{}, 0, false
	}
	out, err := m.net.run(ctx, nftProgram, "-j", "list", "table", "inet", trafficTable)
	if r, fine := parseTraffic(out); err == nil && fine && r.dev == lanDev {
		return r, now, true
	}
	// Missing, half made, or naming a device the local network no longer
	// has: made again. Counts start over, and the table says from when.
	_, _ = m.net.run(ctx, nftProgram, "delete", "table", "inet", trafficTable)
	dir := m.stateDir
	if dir == "" {
		dir = os.TempDir()
	}
	path := filepath.Join(dir, "veilbridge-traffic.nft")
	if err := os.WriteFile(path, []byte(trafficRuleset(lanDev, now)), 0o600); err != nil {
		return trafficRead{}, 0, false
	}
	defer os.Remove(path)
	if _, err := m.net.run(ctx, nftProgram, "-f", path); err != nil {
		return trafficRead{}, 0, false
	}
	return trafficRead{up: map[string]int64{}, down: map[string]int64{}, startedAt: now, dev: lanDev}, now, true
}

// offloaded: flow offloading sends established connections past the forward
// hook, so the counters see only their first packets.
func (m *deviceManager) offloaded(ctx context.Context) bool {
	return m.net.uciGet(ctx, "firewall.@defaults[0].flow_offloading") == "1" ||
		m.net.uciGet(ctx, "firewall.@defaults[0].flow_offloading_hw") == "1"
}

// rememberOwners records which device each address belongs to now. Called
// with m.mu held.
func (m *deviceManager) rememberOwners(owners map[string]string) {
	if m.addrOwner == nil {
		m.addrOwner = map[string]string{}
	}
	for ip, mac := range owners {
		if _, ok := m.addrOwner[ip]; !ok && len(m.addrOwner) >= maxAddrOwners {
			for k := range m.addrOwner { // any one: a bound, not an order
				delete(m.addrOwner, k)
				break
			}
		}
		m.addrOwner[ip] = mac
	}
}
