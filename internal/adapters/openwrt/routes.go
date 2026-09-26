package openwrt

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/netip"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/veilbridge-os/veilbridge/internal/core"
)

// Reading static routes (M3.4, #37). What the owner configured comes from uci
// (`route` and `route6` sections of `network`, draft included, like every
// other screen); whether the kernel is using a route comes from the kernel
// itself, never from netifd's status — measured on both stands, netifd lists
// a route whose gateway the kernel refused, and keeps listing its own gateway
// after something else replaced the route (D-74).

// Kernel route tables as the proc files print them. /proc/net/route is the
// main IPv4 table only, which is exactly the table netifd applies routes to
// and the only one the panel's routes live in (D-73).
const (
	procRoute4 = "/proc/net/route"
	procRoute6 = "/proc/net/ipv6_route"
)

// kernelRoute is one route the kernel holds, in the terms needed to match it
// against a configured one.
type kernelRoute struct {
	prefix  netip.Prefix
	gateway netip.Addr // invalid when the network is reached directly
	metric  int
	device  string
}

// readProc is the seam proc files are read through. Tests replace it; the
// adapter's allow-list of programs stays as it is, because this is a read.
func (m networkManager) readProc(path string) ([]byte, error) {
	if m.procFile != nil {
		return m.procFile(path)
	}
	return os.ReadFile(path)
}

// kernelRoutes returns the IPv4 main table and the IPv6 routes. A file that
// cannot be read yields nothing for its family: every route then reads as not
// active, which is the cautious answer.
func (m networkManager) kernelRoutes() []kernelRoute {
	var out []kernelRoute
	if raw, err := m.readProc(procRoute4); err == nil {
		out = append(out, parseProcRoute(string(raw))...)
	}
	if raw, err := m.readProc(procRoute6); err == nil {
		out = append(out, parseProcRoute6(string(raw))...)
	}
	return out
}

// parseProcRoute reads /proc/net/route. Addresses are printed as the kernel
// holds them in memory — network byte order read as a little-endian word on
// every OpenWrt target we have (x86-64, aarch64); the header line is skipped.
//
//	Iface  Destination Gateway  Flags RefCnt Use Metric Mask     MTU Window IRTT
//	br-lan 0001A8C0    00000000 0001  0      0   0      00FFFFFF 0   0      0
func parseProcRoute(raw string) []kernelRoute {
	var out []kernelRoute
	for i, line := range strings.Split(raw, "\n") {
		f := strings.Fields(line)
		if i == 0 || len(f) < 8 {
			continue
		}
		dst, ok1 := procAddr4(f[1])
		gw, ok2 := procAddr4(f[2])
		mask, ok3 := procAddr4(f[7])
		flags, err1 := strconv.ParseUint(f[3], 16, 32)
		metric, err2 := strconv.Atoi(f[6])
		if !ok1 || !ok2 || !ok3 || err1 != nil || err2 != nil {
			continue
		}
		const rtfUp, rtfGateway = 0x1, 0x2
		if flags&rtfUp == 0 {
			continue
		}
		bits := maskBits(mask)
		if bits < 0 {
			continue
		}
		r := kernelRoute{prefix: netip.PrefixFrom(dst, bits).Masked(), metric: metric, device: f[0]}
		if flags&rtfGateway != 0 {
			r.gateway = gw
		}
		out = append(out, r)
	}
	return out
}

// procAddr4 reads an address the way /proc/net/route prints it: the kernel
// prints the in-memory word with %08X, so the digits depend on the CPU's byte
// order — "0001A8C0" is 192.168.1.0 on x86 and aarch64, and would be
// "C0A80100" on a big-endian MIPS router (ath79 and friends). Writing the
// number back in this CPU's own order recovers the bytes in memory, which are
// in network order on every CPU.
func procAddr4(h string) (netip.Addr, bool) {
	return procAddr4In(h, binary.NativeEndian)
}

func procAddr4In(h string, order binary.ByteOrder) (netip.Addr, bool) {
	if len(h) != 8 {
		return netip.Addr{}, false
	}
	v, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return netip.Addr{}, false
	}
	var a [4]byte
	order.PutUint32(a[:], uint32(v))
	return netip.AddrFrom4(a), true
}

// maskBits turns a contiguous netmask into its length, or -1.
func maskBits(mask netip.Addr) int {
	b := mask.As4()
	v := binary.BigEndian.Uint32(b[:])
	n := 0
	for v&0x80000000 != 0 {
		n++
		v <<= 1
	}
	if v != 0 {
		return -1
	}
	return n
}

// parseProcRoute6 reads /proc/net/ipv6_route: destination, its length, source,
// its length, next hop, metric (all hex), refcount, use, flags, device.
func parseProcRoute6(raw string) []kernelRoute {
	var out []kernelRoute
	for _, line := range strings.Split(raw, "\n") {
		f := strings.Fields(line)
		if len(f) < 10 {
			continue
		}
		dst, ok1 := procAddr6(f[0])
		gw, ok2 := procAddr6(f[4])
		bits, err1 := strconv.ParseUint(f[1], 16, 8)
		metric, err2 := strconv.ParseUint(f[5], 16, 32)
		flags, err3 := strconv.ParseUint(f[8], 16, 32)
		if !ok1 || !ok2 || err1 != nil || err2 != nil || err3 != nil || bits > 128 {
			continue
		}
		const rtfUp, rtfGateway = 0x1, 0x2
		if flags&rtfUp == 0 {
			continue
		}
		r := kernelRoute{prefix: netip.PrefixFrom(dst, int(bits)).Masked(), metric: int(metric), device: f[9]}
		if flags&rtfGateway != 0 {
			r.gateway = gw
		}
		out = append(out, r)
	}
	return out
}

func procAddr6(h string) (netip.Addr, bool) {
	b, err := hex.DecodeString(h)
	if err != nil || len(b) != 16 {
		return netip.Addr{}, false
	}
	return netip.AddrFrom16([16]byte(b)), true
}

// routeSection is a configured route, read from uci, with the target already
// in CIDR form whichever way it was written.
type routeSection struct {
	id          string
	name        string
	disabled    bool
	family      string
	prefix      netip.Prefix // invalid when the target does not parse
	target      string       // as configured, for a route the panel cannot parse
	gateway     netip.Addr
	iface       string
	metric      int
	hasMetric   bool
	netmaskForm bool // written as target + netmask, the older LuCI way
	unsupported []string
}

// routeOptions are the options the panel models; anything else a route has
// is named in Unsupported and makes the route read-only apart from on/off.
var routeOptions = map[string]bool{
	"interface": true, "target": true, "netmask": true, "gateway": true,
	"metric": true, "name": true, "disabled": true,
}

var routeUnsupported = map[string]string{
	"table": core.RouteTable, "source": core.RouteSource, "type": core.RouteType,
	"onlink": core.RouteOnLink, "mtu": core.RouteMTU,
}

// parseRouteSections picks the route sections out of `uci show network`, in
// file order, plus the metric each interface gives routes that set none —
// netifd applies the interface's `metric` to them, so the kernel prints that
// number and a match by the route's own (absent) metric would miss.
func parseRouteSections(show string) ([]routeSection, map[string]int) {
	var routes []routeSection
	ifaceMetric := map[string]int{}
	for _, s := range parseUCISections(show) {
		o := s.options
		if s.kind == "interface" {
			if v, err := strconv.Atoi(o["metric"]); err == nil {
				ifaceMetric[s.id] = v
			}
			continue
		}
		if s.kind != "route" && s.kind != "route6" {
			continue
		}
		r := routeSection{
			id:       s.id,
			name:     o["name"],
			disabled: o["disabled"] == "1",
			family:   "ipv4",
			target:   o["target"],
			iface:    o["interface"],
		}
		if s.kind == "route6" {
			r.family = "ipv6"
		}
		r.prefix, r.netmaskForm = routeTarget(o["target"], o["netmask"], r.family)
		if gw, err := netip.ParseAddr(o["gateway"]); err == nil {
			r.gateway = gw
		}
		if v, err := strconv.Atoi(o["metric"]); err == nil {
			r.metric, r.hasMetric = v, true
		}
		unsupported := map[string]bool{}
		for opt := range o {
			if routeOptions[opt] {
				continue
			}
			if name, ok := routeUnsupported[opt]; ok {
				unsupported[name] = true
			} else {
				unsupported[core.RouteOther] = true
			}
		}
		if !r.prefix.IsValid() {
			unsupported[core.RouteOther] = true
		}
		r.unsupported = sortedKeys(unsupported)
		routes = append(routes, r)
	}
	return routes, ifaceMetric
}

// routeTarget reads a target in either form netifd accepts: CIDR in
// `target`, or an address in `target` plus `netmask`. Host bits are cleared
// the way netifd clears them (measured: 198.51.100.7/24 became .0/24).
func routeTarget(target, netmask, family string) (netip.Prefix, bool) {
	target = strings.TrimSpace(target)
	if target == "" {
		return netip.Prefix{}, false
	}
	if p, err := netip.ParsePrefix(target); err == nil {
		return p.Masked(), false
	}
	a, err := netip.ParseAddr(target)
	if err != nil {
		return netip.Prefix{}, false
	}
	if netmask != "" && a.Is4() {
		m, err := netip.ParseAddr(netmask)
		if err != nil || !m.Is4() {
			return netip.Prefix{}, false
		}
		bits := maskBits(m)
		if bits < 0 {
			return netip.Prefix{}, false
		}
		return netip.PrefixFrom(a, bits).Masked(), true
	}
	_ = family
	return netip.PrefixFrom(a, a.BitLen()), false
}

// effectiveMetric is the number the kernel shows for this route.
func (r routeSection) effectiveMetric(ifaceMetric map[string]int) int {
	if r.hasMetric {
		return r.metric
	}
	return ifaceMetric[r.iface]
}

// matches reports whether a kernel route is this configured route.
func (r routeSection) matches(k kernelRoute, device string, ifaceMetric map[string]int) bool {
	if !r.prefix.IsValid() || k.prefix != r.prefix || k.device != device {
		return false
	}
	if k.metric != r.effectiveMetric(ifaceMetric) {
		return false
	}
	if r.gateway.IsValid() {
		return k.gateway == r.gateway
	}
	return !k.gateway.IsValid()
}

// StaticRoutes answers the routes screen.
func (m networkManager) StaticRoutes() (core.RoutesStatus, error) {
	if m.run == nil {
		return core.RoutesStatus{}, core.ErrNotImplemented
	}
	ctx, cancel := context.WithTimeout(context.Background(), firewallTimeout)
	defer cancel()
	out, err := m.run(ctx, "uci", "-q", "show", "network")
	if err != nil {
		return core.RoutesStatus{}, err
	}
	sections, ifaceMetric := parseRouteSections(string(out))
	ifaces := m.routeInterfaces()
	devices := map[string]string{}
	for _, i := range ifaces {
		devices[i.Name] = i.device
	}
	kernel := m.kernelRoutes()

	st := core.RoutesStatus{Routes: []core.StaticRoute{}, Interfaces: []core.RouteInterface{}}
	for _, r := range sections {
		sr := core.StaticRoute{
			ID:          r.id,
			Name:        r.name,
			Enabled:     !r.disabled,
			Family:      r.family,
			Target:      r.target,
			Interface:   r.iface,
			Metric:      r.metric,
			Unsupported: r.unsupported,
		}
		if r.prefix.IsValid() {
			sr.Target = r.prefix.String()
		}
		if r.gateway.IsValid() {
			sr.Gateway = r.gateway.String()
		}
		if !r.disabled {
			for _, k := range kernel {
				if r.matches(k, devices[r.iface], ifaceMetric) {
					sr.Active = true
					break
				}
			}
		}
		st.Routes = append(st.Routes, sr)
	}
	for _, i := range ifaces {
		st.Interfaces = append(st.Interfaces, i.RouteInterface)
	}
	return st, nil
}

// routeInterface is a connection with the kernel device its routes use.
type routeInterface struct {
	core.RouteInterface
	device string
}

// routeInterfaces lists the connections a route can use: everything netifd
// knows except loopback. A failure to ask yields none, and every route then
// reads as not active — cautious, not wrong.
func (m networkManager) routeInterfaces() []routeInterface {
	list := m.lookupInterfaces
	if list == nil {
		list = m.Interfaces
	}
	ifaces, err := list()
	if err != nil {
		return nil
	}
	var out []routeInterface
	for _, i := range ifaces {
		if loopbackNames[i.Name] {
			continue
		}
		v4 := i.IPv4
		if v4 == nil {
			v4 = []string{}
		}
		out = append(out, routeInterface{
			RouteInterface: core.RouteInterface{Name: i.Name, Up: i.Up, IPv4: v4},
			device:         i.Device,
		})
	}
	return out
}

// sortedKeys returns a set as a sorted, never-nil list.
func sortedKeys(set map[string]bool) []string {
	out := []string{}
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// routeWords names a route the way a person reads it, in no language:
// "10.8.0.0/24 → 192.168.1.2 (lan)", or "10.8.0.0/24 (lan)" for a network
// reached directly. An English "via" landed in the Russian apply bar.
func routeWords(target, gateway, iface string) string {
	s := target
	if gateway != "" {
		s += " → " + gateway
	}
	if iface != "" {
		s += fmt.Sprintf(" (%s)", iface)
	}
	return s
}

var _ core.RouteReader = networkManager{}
