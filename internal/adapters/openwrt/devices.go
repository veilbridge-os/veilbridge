package openwrt

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veilbridge-os/veilbridge/internal/adapters/openwrt/ubus"
	"github.com/veilbridge-os/veilbridge/internal/core"
)

// Devices on the local network (M4, #51).
//
// The router keeps what it knows about its clients in four places, and none
// of them is enough alone. All of this was measured on both branches (#50)
// before it was written:
//
//   - the lease file knows the name a device gave and its IPv4 lease, but a
//     lease outlives the device by hours;
//   - the neighbour table knows the addresses a device used, IPv6 included,
//     but it keeps no useful age (BusyBox `ip neigh` prints "used 0/0/0"), a
//     device that left an hour ago sits there STALE, and a sleeping phone that
//     is still on the Wi-Fi shows FAILED. It is a source of addresses, not of
//     presence. It is read from the kernel over netlink, not by running `ip`;
//   - the access points (hostapd on the bus) know who is associated right now,
//     with band and signal. For Wi-Fi that is the answer to "online" (D-85).
//     They are found as the radio interfaces in /sys/class/net, not by listing
//     the bus: the bus client may only `call`;
//   - the bridge's forwarding table (/sys/class/net/<br>/brforward) knows the
//     port each address was last heard on and how long ago, in hundredths of
//     a second, for cable and Wi-Fi alike. Entries live for the bridge's
//     ageing time (300 s on both branches). That is the only "last heard N
//     seconds ago" the router offers.
//
// What is heard is remembered in memory only (D-13), on the monotonic clock:
// the router's wall clock jumps by hours when it is synced after a boot (#50),
// and "last seen" must not jump with it.

const devicesTimeout = 10 * time.Second

// maxRemembered bounds the in-memory "last seen" record. Phones that change
// their private address would otherwise grow it for as long as the daemon runs.
const maxRemembered = 2048

// fdbEntrySize is the size of struct __fdb_entry in <linux/if_bridge.h>:
// mac[6], port_no, is_local, ageing_timer_value (u32, CPU byte order, in
// USER_HZ = 1/100 s), port_hi, pad, unused[2].
const fdbEntrySize = 16

type heard struct {
	at   time.Time
	link core.DeviceLink
}

type deviceManager struct {
	net networkManager
	bus *ubus.Client
	// neigh is the seam the neighbour table is read through (netlink on the
	// device, captured tables in tests).
	neigh func(dev string) ([]neighbour, error)
	// sys is the seam /sys is read through; tests hand in captured files.
	sys sysReader
	now func() time.Time

	mu      sync.Mutex
	started time.Time
	seen    map[string]heard
}

// sysReader reads the kernel's view of the bridge. Its methods mirror the
// three things done with /sys here, so a test can describe a bridge in a map.
type sysReader interface {
	ReadFile(path string) ([]byte, error)
	ReadDir(path string) ([]string, error)
	Exists(path string) bool
}

type realSys struct{}

func (realSys) ReadFile(p string) ([]byte, error) { return os.ReadFile(p) }
func (realSys) ReadDir(p string) ([]string, error) {
	entries, err := os.ReadDir(p)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	return names, nil
}
func (realSys) Exists(p string) bool { _, err := os.Stat(p); return err == nil }

func newDeviceManager(n networkManager, bus *ubus.Client) *deviceManager {
	return &deviceManager{net: n, bus: bus, neigh: kernelNeighbours, sys: realSys{}, now: time.Now,
		started: time.Now(), seen: map[string]heard{}}
}

// ObserveDevices takes a look and remembers who was heard, for the daemon's
// timer. Errors are dropped: a missed look is a gap, not a fault.
func (m *deviceManager) ObserveDevices() { _, _ = m.ListDevices() }

// observation is one device as the sources describe it before merging.
type observation struct {
	ips      []string
	reported string
	reserved string
	online   bool
	at       time.Time // zero when nothing says when it was heard
	link     core.DeviceLink
}

func (m *deviceManager) ListDevices() (core.DeviceList, error) {
	lanDev, err := m.lanDevice()
	if err != nil {
		return core.DeviceList{}, err
	}
	if m.net.run == nil {
		return core.DeviceList{}, core.ErrNotImplemented
	}
	ctx, cancel := context.WithTimeout(context.Background(), devicesTimeout)
	defer cancel()
	now := m.now()

	obs := map[string]*observation{}
	get := func(mac string) *observation {
		mac, err := core.NormalizeMAC(mac)
		if err != nil {
			return nil
		}
		o := obs[mac]
		if o == nil {
			o = &observation{}
			obs[mac] = o
		}
		return o
	}
	later := func(o *observation, t time.Time) {
		if t.After(o.at) {
			o.at = t
		}
	}

	// Leases: the name the device gave, and its address while the lease runs.
	for _, l := range m.net.leases(ctx) {
		if o := get(l.MAC); o != nil {
			if l.Hostname != "" {
				o.reported = l.Hostname
			}
			if l.ExpiresSec > 0 {
				o.ips = append(o.ips, l.IP)
			}
		}
	}
	for _, r := range m.net.reserved(ctx) {
		if o := get(r.MAC); o != nil {
			o.reserved = r.IP
		}
	}

	// Access points: associated now is online now, whatever else says.
	aps := m.accessPoints(ctx)
	bands := map[string]string{}
	for _, ap := range aps {
		bands[ap.iface] = ap.band
	}
	for _, ap := range aps {
		for mac, c := range ap.clients {
			if o := get(mac); o != nil {
				o.online = true
				o.link = core.DeviceLink{Kind: core.LinkWiFi, Band: ap.band, SignalDBm: c.signal}
				later(o, now)
			}
		}
	}

	// Bridge: which port, and how long ago.
	fdb := m.forwarding(lanDev, bands)
	for _, e := range fdb {
		if e.local {
			continue // the router's own addresses
		}
		o := get(e.mac)
		if o == nil {
			continue
		}
		later(o, now.Add(-e.age))
		if o.link.Kind == core.LinkWiFi && o.online {
			continue // the access point already said more
		}
		if band, wifi := e.wifiBand, e.wifi; wifi {
			// Heard on an access point that no longer has it: it left. The
			// bridge keeps the entry for minutes; the access point is the
			// authority on Wi-Fi (D-85).
			o.link = core.DeviceLink{Kind: core.LinkWiFi, Band: band}
			continue
		}
		o.link = core.DeviceLink{Kind: core.LinkCable}
		o.online = true
	}

	// Neighbours: addresses, IPv6 included; presence only where there is no
	// bridge to ask (a local network on a single port).
	neigh, _ := m.neigh(lanDev)
	for _, n := range neigh {
		o := get(n.mac)
		if o == nil {
			continue
		}
		o.ips = append(o.ips, n.ip)
		if len(fdb) == 0 && n.fresh {
			o.online = true
			later(o, now)
			if o.link.Kind == "" {
				o.link = core.DeviceLink{Kind: core.LinkCable}
			}
		}
	}
	// Devices whose internet is off stay on the list even when nothing hears
	// them: a block you cannot see is a block you cannot lift (#53).
	blocked := m.net.blocked(ctx)
	for mac := range blocked {
		get(mac)
	}
	scheduled := m.net.schedulesNow(ctx)
	for mac := range scheduled {
		get(mac)
	}
	clock := m.clock(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	for mac, o := range obs {
		if prev, ok := m.seen[mac]; ok {
			if o.at.IsZero() || prev.at.After(o.at) {
				o.at = prev.at
			}
			if o.link.Kind == "" {
				// Not heard now: say how it was connected when it was, but
				// not with what signal — that was then.
				o.link = core.DeviceLink{Kind: prev.link.Kind, Band: prev.link.Band}
			}
		}
		if !o.at.IsZero() {
			m.remember(mac, heard{at: o.at, link: o.link})
		}
	}
	// Heard earlier and gone from every source since: still worth a line
	// ("was here 40 minutes ago").
	for mac, h := range m.seen {
		if _, ok := obs[mac]; !ok {
			obs[mac] = &observation{at: h.at, link: core.DeviceLink{Kind: h.link.Kind, Band: h.link.Band}}
		}
	}

	out := core.DeviceList{Devices: make([]core.Device, 0, len(obs)),
		WatchingSec: int64(now.Sub(m.started) / time.Second), Clock: clock}
	for mac, o := range obs {
		d := core.Device{MAC: mac, ReportedName: o.reported, ReservedIP: o.reserved,
			Online: o.online, IPs: sortIPs(o.ips), Link: o.link, Internet: core.InternetAllowed}
		if blocked[mac] {
			d.Internet = core.InternetBlocked
		}
		if s, ok := scheduled[mac]; ok {
			d.Schedule = &s
			core.ApplySchedule(&d, clock)
		}
		if d.Link.Kind == "" {
			d.Link.Kind = core.LinkUnknown
		}
		if !o.online && !o.at.IsZero() {
			ago := int64(now.Sub(o.at) / time.Second)
			if ago < 0 {
				ago = 0
			}
			d.LastSeenSec = &ago
		}
		out.Devices = append(out.Devices, d)
	}
	sort.Slice(out.Devices, func(i, j int) bool { return out.Devices[i].MAC < out.Devices[j].MAC })
	return out, nil
}

// remember keeps the record bounded by dropping the longest-unheard entry.
func (m *deviceManager) remember(mac string, h heard) {
	if _, ok := m.seen[mac]; !ok && len(m.seen) >= maxRemembered {
		var oldest string
		for k, v := range m.seen {
			if oldest == "" || v.at.Before(m.seen[oldest].at) {
				oldest = k
			}
		}
		delete(m.seen, oldest)
	}
	m.seen[mac] = h
}

func (m *deviceManager) lanDevice() (string, error) {
	list := m.net.lookupInterfaces
	if list == nil {
		list = m.net.Interfaces
	}
	ifaces, err := list()
	if err != nil {
		return "", err
	}
	for _, i := range ifaces {
		if i.Name == lanSection {
			if i.Device == "" {
				return "", errors.New("openwrt: the local network has no device")
			}
			return i.Device, nil
		}
	}
	return "", core.ErrNoLAN
}

type apClient struct{ signal int }

type accessPoint struct {
	iface   string
	band    string
	clients map[string]apClient
}

// accessPoints asks every access point who is associated. The access points
// are the radio interfaces; a radio that is not an access point (a station,
// a mesh link) has no object on the bus and is skipped. A router without
// radios has none, and that is an empty answer, not an error.
func (m *deviceManager) accessPoints(ctx context.Context) []accessPoint {
	if m.bus == nil {
		return nil
	}
	names, err := m.sys.ReadDir("/sys/class/net")
	if err != nil {
		return nil
	}
	var out []accessPoint
	for _, iface := range names {
		if !m.sys.Exists("/sys/class/net/" + iface + "/phy80211") {
			continue
		}
		var body json.RawMessage
		if err := m.bus.Call(ctx, "hostapd."+iface, "get_clients", &body); err != nil {
			continue
		}
		ap, ok := parseClients(body)
		if !ok {
			continue
		}
		ap.iface = iface
		out = append(out, ap)
	}
	return out
}

// parseClients reads `get_clients`. Shape captured on 25.12.5 (empty) and,
// per client, as hostapd's ubus code writes it: a map keyed by address with
// "assoc" and "signal" among many fields.
func parseClients(body []byte) (accessPoint, bool) {
	var v struct {
		Freq    int `json:"freq"`
		Clients map[string]struct {
			Assoc  *bool `json:"assoc"`
			Signal int   `json:"signal"`
		} `json:"clients"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return accessPoint{}, false
	}
	ap := accessPoint{band: bandOf(v.Freq), clients: map[string]apClient{}}
	for mac, c := range v.Clients {
		// Authenticated but not associated is a device on its way in or
		// out, not one that is here.
		if c.Assoc != nil && !*c.Assoc {
			continue
		}
		ap.clients[mac] = apClient{signal: c.Signal}
	}
	return ap, true
}

func bandOf(freqMHz int) string {
	switch {
	case freqMHz <= 0:
		return ""
	case freqMHz < 3000:
		return "2.4"
	case freqMHz < 5925:
		return "5"
	default:
		return "6"
	}
}

type fdbEntry struct {
	mac      string
	local    bool
	age      time.Duration
	wifi     bool
	wifiBand string
}

// forwarding reads the bridge's forwarding table. A local network that is not
// a bridge has none, which the caller treats as "ask the neighbours".
func (m *deviceManager) forwarding(bridge string, bands map[string]string) []fdbEntry {
	base := "/sys/class/net/" + bridge
	raw, err := m.sys.ReadFile(base + "/brforward")
	if err != nil {
		return nil
	}
	ports := map[int]string{}
	if names, err := m.sys.ReadDir(base + "/brif"); err == nil {
		for _, p := range names {
			b, err := m.sys.ReadFile(base + "/brif/" + p + "/port_no")
			if err != nil {
				continue
			}
			if n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 0, 32); err == nil {
				ports[int(n)] = p
			}
		}
	}
	return parseForwarding(raw, ports, func(port string) bool {
		return m.sys.Exists("/sys/class/net/" + port + "/phy80211")
	}, func(port string) string { return bands[port] })
}

func parseForwarding(raw []byte, ports map[int]string, isRadio func(string) bool, band func(string) string) []fdbEntry {
	var out []fdbEntry
	radio := map[string]bool{}
	bands := map[string]string{}
	for off := 0; off+fdbEntrySize <= len(raw); off += fdbEntrySize {
		rec := raw[off : off+fdbEntrySize]
		e := fdbEntry{
			mac:   net.HardwareAddr(rec[0:6]).String(),
			local: rec[7] != 0,
			age:   time.Duration(binary.NativeEndian.Uint32(rec[8:12])) * 10 * time.Millisecond,
		}
		port := ports[int(rec[12])<<8|int(rec[6])]
		if port != "" && !e.local {
			if _, ok := radio[port]; !ok {
				radio[port] = isRadio(port)
				if radio[port] {
					bands[port] = band(port)
				}
			}
			e.wifi, e.wifiBand = radio[port], bands[port]
		}
		out = append(out, e)
	}
	return out
}

type neighbour struct {
	ip, mac string
	fresh   bool
}

// Neighbour states, <linux/neighbour.h>.
const (
	nudReachable = 0x02
	nudStale     = 0x04
	nudDelay     = 0x08
	nudProbe     = 0x10
	nudPermanent = 0x80
)

// Attribute types of a neighbour message, <linux/neighbour.h>.
const (
	ndaDst    = 1
	ndaLLAddr = 2
)

// ndMsgLen is struct ndmsg: family, pad, pad(2), ifindex(4), state(2),
// flags, type.
const ndMsgLen = 12

// parseNeighMsg reads one RTM_NEWNEIGH payload: struct ndmsg, then route
// attributes, all in the CPU's byte order. Entries on other interfaces, without
// a hardware address (INCOMPLETE, FAILED), or link-local are dropped — the
// first belong to the uplink's network (#50), the last mean nothing to a person.
func parseNeighMsg(b []byte, ifindex int) (neighbour, bool) {
	if len(b) < ndMsgLen {
		return neighbour{}, false
	}
	if int(int32(binary.NativeEndian.Uint32(b[4:8]))) != ifindex {
		return neighbour{}, false
	}
	state := binary.NativeEndian.Uint16(b[8:10])
	var ip net.IP
	var mac net.HardwareAddr
	for a := b[ndMsgLen:]; len(a) >= 4; {
		l := int(binary.NativeEndian.Uint16(a[0:2]))
		if l < 4 || l > len(a) {
			break
		}
		switch binary.NativeEndian.Uint16(a[2:4]) {
		case ndaDst:
			ip = net.IP(append([]byte(nil), a[4:l]...))
		case ndaLLAddr:
			mac = net.HardwareAddr(append([]byte(nil), a[4:l]...))
		}
		a = a[min((l+3)&^3, len(a)):]
	}
	if len(mac) != 6 || ip == nil || ip.IsLinkLocalUnicast() || ip.To16() == nil {
		return neighbour{}, false
	}
	n := neighbour{ip: ip.String(), mac: mac.String()}
	switch {
	case state&(nudReachable|nudDelay|nudProbe) != 0:
		n.fresh = true
	case state&(nudStale|nudPermanent) != 0:
	default:
		return neighbour{}, false
	}
	return n, true
}

// sortIPs puts IPv4 first and drops duplicates (the lease and the neighbour
// table usually both know the IPv4 address).
func sortIPs(ips []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, s := range ips {
		if ip := net.ParseIP(s); ip != nil && !seen[ip.String()] {
			seen[ip.String()] = true
			out = append(out, ip.String())
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := net.ParseIP(out[i]).To4() != nil, net.ParseIP(out[j]).To4() != nil
		if a != b {
			return a
		}
		return out[i] < out[j]
	})
	return out
}
