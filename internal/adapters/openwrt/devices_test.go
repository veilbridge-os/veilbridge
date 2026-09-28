package openwrt

import (
	"encoding/binary"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/veilbridge-os/veilbridge/internal/adapters/openwrt/ubus"
	"github.com/veilbridge-os/veilbridge/internal/core"
)

// #51. The shapes below (with addresses swapped for documentation ones:
// RFC 5737, 2001:db8::/32, 00:00:5e:00:53:xx of RFC 7042) were captured on the stands (#50 and 28.09):
// brforward on the Cudy (25.12.5, arm64) and the VM (23.05.5, x86-64), `ip
// neigh` on both, `get_clients` on the Cudy — empty, and with an iPhone
// associated (28.09).

// fdbRecord builds one struct __fdb_entry the way the kernel writes it.
func fdbRecord(mac string, port int, local bool, ageCentis uint32) []byte {
	rec := make([]byte, fdbEntrySize)
	hw, _ := net.ParseMAC(mac)
	copy(rec, hw)
	rec[6] = byte(port)
	if local {
		rec[7] = 1
	}
	binary.NativeEndian.PutUint32(rec[8:12], ageCentis)
	return rec
}

type mapSys struct {
	files map[string][]byte
	dirs  map[string][]string
}

func (s mapSys) ReadFile(p string) ([]byte, error) {
	if b, ok := s.files[p]; ok {
		return b, nil
	}
	return nil, os.ErrNotExist
}
func (s mapSys) ReadDir(p string) ([]string, error) {
	if d, ok := s.dirs[p]; ok {
		return d, nil
	}
	return nil, os.ErrNotExist
}
func (s mapSys) Exists(p string) bool { _, ok := s.files[p]; return ok }

// cudy describes the reference router's bridge: four cable ports and two
// radios (port numbers as read from brif/*/port_no).
func cudy(fdb ...[]byte) mapSys {
	s := mapSys{files: map[string][]byte{}, dirs: map[string][]string{
		"/sys/class/net/br-lan/brif": {"lan1", "lan2", "lan3", "lan4", "phy0-ap0", "phy1-ap0"},
		"/sys/class/net":             {"br-lan", "eth0", "lan1", "lan2", "lan3", "lan4", "lo", "phy0-ap0", "phy1-ap0", "wan"},
	}}
	for i, p := range []string{"lan1", "lan2", "lan3", "lan4", "phy0-ap0", "phy1-ap0"} {
		s.files["/sys/class/net/br-lan/brif/"+p+"/port_no"] = []byte("0x" + string(rune('1'+i)) + "\n")
	}
	s.files["/sys/class/net/phy0-ap0/phy80211"] = nil
	s.files["/sys/class/net/phy1-ap0/phy80211"] = nil
	var raw []byte
	for _, r := range fdb {
		raw = append(raw, r...)
	}
	s.files["/sys/class/net/br-lan/brforward"] = raw
	return s
}

// Captured on the Cudy (25.12.5) with an iPhone associated on 5 GHz, 28.09;
// the hardware address swapped for a documentation one, the capability
// blocks cut.
const clients5 = `{
	"freq": 5180,
	"clients": {
		"02:00:5e:00:53:d6": {
			"auth": true, "assoc": true, "authorized": true, "preauth": false,
			"wds": false, "wmm": true, "ht": true, "vht": true, "he": true,
			"wps": false, "mfp": false, "mbo": false,
			"rrm": [0, 0, 0, 0, 0],
			"extended_capabilities": [0, 0, 0, 0, 0, 0, 0, 64],
			"aid": 1,
			"bytes": {"rx": 91497, "tx": 125286},
			"airtime": {"rx": 90759, "tx": 48949},
			"packets": {"rx": 474, "tx": 250},
			"rate": {"rx": 120090000, "tx": 120090000},
			"signal": -48
		}
	}
}`

const clients24Empty = `{
	"freq": 2412,
	"clients": {
		
	}
}`

// Lease expiries are wall-clock times, compared with the real clock the way
// dnsmasq writes them: fixtures use one long past and one far ahead.

type devFixture struct {
	t      *testing.T
	runner *fakeRunner
	m      *deviceManager
	neigh  []neighbour
	clock  time.Time
}

func newDevFixture(t *testing.T, sys mapSys, leases string) *devFixture {
	t.Helper()
	f := newFakeRunner()
	leaseFile := filepath.Join(t.TempDir(), "dhcp.leases")
	if err := os.WriteFile(leaseFile, []byte(leases), 0o600); err != nil {
		t.Fatal(err)
	}
	f.out["uci -q get dhcp.@dnsmasq[0].leasefile"] = []byte(leaseFile + "\n")
	f.out["/bin/ubus call hostapd.phy0-ap0 get_clients"] = []byte(clients24Empty)
	f.out["/bin/ubus call hostapd.phy1-ap0 get_clients"] = []byte(clients5)
	n := networkManager{run: f.run, lookupInterfaces: func() ([]core.NetworkInterface, error) {
		return []core.NetworkInterface{{Name: "wan", Device: "wan"}, {Name: "lan", Device: "br-lan"}}, nil
	}}
	fx := &devFixture{t: t, runner: f, clock: time.Unix(1790012600, 0)}
	fx.m = newDeviceManager(n, ubus.NewWithRunner(f.run))
	fx.m.sys = sys
	fx.m.neigh = func(string) ([]neighbour, error) { return fx.neigh, nil }
	fx.m.started = fx.clock
	fx.m.now = func() time.Time { return fx.clock }
	return fx
}

func (fx *devFixture) list() map[string]core.Device {
	fx.t.Helper()
	l, err := fx.m.ListDevices()
	if err != nil {
		fx.t.Fatalf("ListDevices: %v", err)
	}
	// The fake runner answers anything; the real ones refuse programs outside
	// their allow-lists. The first build of this list ran `ip` and `ubus list`,
	// passed every test here and showed no addresses and no Wi-Fi on the stand.
	for _, c := range fx.runner.calls {
		prog := strings.Fields(c)[0]
		if !allowedCommands[prog] && prog != "/bin/ubus" {
			fx.t.Errorf("ran %q: the device would refuse it (not in an allow-list)", c)
		}
		if prog == "/bin/ubus" && !strings.HasPrefix(c, "/bin/ubus call ") {
			fx.t.Errorf("ran %q: the bus client only calls", c)
		}
		// Only radios are asked: every other interface would be a process
		// started on the router every minute for a "Not found".
		if obj, ok := strings.CutPrefix(c, "/bin/ubus call hostapd."); ok {
			iface := strings.Fields(obj)[0]
			if !fx.m.sys.Exists("/sys/class/net/" + iface + "/phy80211") {
				fx.t.Errorf("asked %q, which is not a radio", c)
			}
		}
	}
	out := map[string]core.Device{}
	for _, d := range l.Devices {
		out[d.MAC] = d
	}
	return out
}

// ndRecord builds one RTM_NEWNEIGH payload the way the kernel writes it.
func ndRecord(ifindex int, state uint16, ip, mac string) []byte {
	b := make([]byte, ndMsgLen)
	binary.NativeEndian.PutUint32(b[4:8], uint32(ifindex))
	binary.NativeEndian.PutUint16(b[8:10], state)
	attr := func(typ uint16, v []byte) {
		a := make([]byte, 4+len(v))
		binary.NativeEndian.PutUint16(a[0:2], uint16(4+len(v)))
		binary.NativeEndian.PutUint16(a[2:4], typ)
		copy(a[4:], v)
		for len(a)%4 != 0 {
			a = append(a, 0)
		}
		b = append(b, a...)
	}
	p := net.ParseIP(ip)
	if p4 := p.To4(); p4 != nil {
		p = p4
	}
	attr(ndaDst, p)
	if mac != "" {
		hw, _ := net.ParseMAC(mac)
		attr(ndaLLAddr, hw)
	}
	return b
}

// neighCudy is the table captured on the Cudy (as `ip neigh` printed it),
// in the kernel's own form: br-lan is ifindex 12, wan 5. The phone's private
// address is on the LAN side; the Mac and the home router are on the uplink
// and must not become devices.
func neighCudy() []neighbour {
	recs := [][]byte{
		ndRecord(12, 0x20, "192.168.1.144", ""), // FAILED, no address
		ndRecord(5, nudDelay, "198.51.100.7", "00:00:5e:00:53:07"),
		ndRecord(12, nudStale, "fe80::14c4:8ab5:66cc:42c0", "02:00:5e:00:53:d6"),
		ndRecord(12, nudStale, "2001:db8:1:c:1c2a:7aff:fe11:2233", "02:00:5e:00:53:d6"),
		ndRecord(12, nudReachable, "192.168.1.188", "02:00:5e:00:53:d6"),
		ndRecord(5, nudStale, "2001:db8:1::1", "00:00:5e:00:53:01"),
	}
	var out []neighbour
	for _, r := range recs {
		if n, ok := parseNeighMsg(r, 12); ok {
			out = append(out, n)
		}
	}
	return out
}

func TestAPhoneOnWiFiIsOnlineWithBandSignalAndBothAddresses(t *testing.T) {
	fx := newDevFixture(t, cudy(
		fdbRecord("00:00:5e:00:53:42", 1, true, 0), // the router's own
		fdbRecord("02:00:5e:00:53:d6", 6, false, 150),
	), "4102444800 02:00:5e:00:53:d6 192.168.1.188 iPhone 01:02:00:5e:00:53:d6\n")
	fx.neigh = neighCudy()

	got := fx.list()
	if len(got) != 1 {
		t.Fatalf("devices = %v, want only the phone (not the router, not the uplink's hosts)", got)
	}
	d := got["02:00:5e:00:53:d6"]
	if !d.Online || d.Link.Kind != core.LinkWiFi || d.Link.Band != "5" || d.Link.SignalDBm != -48 {
		t.Errorf("phone = %+v, want online on Wi-Fi 5 GHz at -48 dBm", d)
	}
	if d.ReportedName != "iPhone" {
		t.Errorf("reportedName = %q", d.ReportedName)
	}
	want := []string{"192.168.1.188", "2001:db8:1:c:1c2a:7aff:fe11:2233"}
	if strings.Join(d.IPs, ",") != strings.Join(want, ",") {
		t.Errorf("ips = %v, want %v (IPv4 once, then IPv6, no link-local)", d.IPs, want)
	}
	if d.LastSeenSec != nil {
		t.Errorf("an online device has lastSeenSec %d", *d.LastSeenSec)
	}
}

// A sleeping phone that is still associated shows FAILED in the neighbour
// table (#50); the access point is the authority.
func TestAssociatedIsOnlineEvenWhenTheNeighbourTableSaysFailed(t *testing.T) {
	fx := newDevFixture(t, cudy(), "")
	if _, ok := parseNeighMsg(ndRecord(12, 0x20, "192.168.1.188", "02:00:5e:00:53:d6"), 12); ok {
		t.Fatal("a FAILED entry was read as a neighbour")
	}
	d := fx.list()["02:00:5e:00:53:d6"]
	if !d.Online {
		t.Errorf("associated phone = %+v, want online", d)
	}
	if len(d.IPs) != 0 {
		t.Errorf("a FAILED neighbour entry gave addresses %v", d.IPs)
	}
}

// The bridge keeps a device that left the Wi-Fi for minutes. It is not online.
func TestHeardOnARadioButNoLongerAssociatedHasLeft(t *testing.T) {
	fx := newDevFixture(t, cudy(fdbRecord("02:11:22:33:44:55", 5, false, 9000)), "")
	d := fx.list()["02:11:22:33:44:55"]
	if d.Online {
		t.Errorf("device = %+v: on a radio port but not associated must not be online", d)
	}
	if d.Link.Kind != core.LinkWiFi || d.Link.Band != "2.4" {
		t.Errorf("link = %+v, want Wi-Fi 2.4 GHz (the port it was heard on)", d.Link)
	}
	if d.LastSeenSec == nil || *d.LastSeenSec != 90 {
		t.Errorf("lastSeenSec = %v, want 90 (9000 hundredths)", d.LastSeenSec)
	}
}

func TestACableDeviceIsOnlineWhileTheBridgeHearsIt(t *testing.T) {
	fx := newDevFixture(t, cudy(fdbRecord("00:00:5e:00:53:10", 2, false, 1273)), "")
	d := fx.list()["00:00:5e:00:53:10"]
	if !d.Online || d.Link.Kind != core.LinkCable {
		t.Errorf("device = %+v, want online on the cable", d)
	}
}

// "Last seen" survives the bridge forgetting the device, and is measured on
// the monotonic clock of the fixture — a wall-clock jump does not move it.
func TestLastSeenIsRememberedAfterTheBridgeForgets(t *testing.T) {
	sys := cudy(fdbRecord("00:00:5e:00:53:10", 2, false, 1000))
	fx := newDevFixture(t, sys, "")
	fx.list()
	sys.files["/sys/class/net/br-lan/brforward"] = nil
	fx.clock = fx.clock.Add(40 * time.Minute)
	d := fx.list()["00:00:5e:00:53:10"]
	if d.Online {
		t.Errorf("not heard for 40 minutes, still online: %+v", d)
	}
	if d.LastSeenSec == nil || *d.LastSeenSec != 40*60+10 {
		t.Errorf("lastSeenSec = %v, want %d", d.LastSeenSec, 40*60+10)
	}
	if d.Link.Kind != core.LinkCable {
		t.Errorf("link = %+v, want the cable it was last heard on", d.Link)
	}
}

// A signal reading is a now-fact; after the device left, it is not repeated —
// also while its lease still runs and keeps it in the list.
func TestSignalIsNotRememberedAfterTheDeviceLeaves(t *testing.T) {
	fx := newDevFixture(t, cudy(), "4102444800 02:00:5e:00:53:d6 192.168.1.188 iPhone 01:02\n")
	fx.list()
	fx.runner.out["/bin/ubus call hostapd.phy1-ap0 get_clients"] = []byte(`{"freq": 5180, "clients": {}}`)
	fx.clock = fx.clock.Add(5 * time.Minute)
	d := fx.list()["02:00:5e:00:53:d6"]
	if d.Online || d.Link.SignalDBm != 0 || d.Link.Band != "5" {
		t.Errorf("left phone = %+v, want offline, band kept, no signal", d)
	}
	if d.LastSeenSec == nil || *d.LastSeenSec != 300 {
		t.Errorf("lastSeenSec = %v, want 300", d.LastSeenSec)
	}
}

// Authenticated but not associated is a device on its way in or out.
func TestNotAssociatedIsNotOnline(t *testing.T) {
	fx := newDevFixture(t, cudy(), "")
	fx.runner.out["/bin/ubus call hostapd.phy1-ap0 get_clients"] = []byte(
		`{"freq": 5180, "clients": {"02:aa:bb:cc:dd:ee": {"auth": true, "assoc": false, "signal": -70}}}`)
	if d, ok := fx.list()["02:aa:bb:cc:dd:ee"]; ok && d.Online {
		t.Errorf("auth-only client = %+v, want not online", d)
	}
}

// A lease outlives the device; an expired one gives a name but no address,
// and says nothing about presence.
func TestALeaseAloneIsNotPresence(t *testing.T) {
	fx := newDevFixture(t, cudy(), ""+
		"1000000000 02:0d:33:7a:55:c2 192.168.1.187 old-tablet 01:02\n"+
		"4102444800 00:00:5e:00:53:21 192.168.1.163 * 01:00\n")
	fx.runner.out["/bin/ubus call hostapd.phy1-ap0 get_clients"] = []byte(`{"freq": 5180, "clients": {}}`)
	got := fx.list()
	old := got["02:0d:33:7a:55:c2"]
	if old.Online || len(old.IPs) != 0 || old.ReportedName != "old-tablet" {
		t.Errorf("expired lease = %+v, want offline, no address, the name it gave", old)
	}
	if old.LastSeenSec != nil {
		t.Errorf("a lease says when an address was handed out, not when the device was heard: %v", *old.LastSeenSec)
	}
	cur := got["00:00:5e:00:53:21"]
	if cur.Online || strings.Join(cur.IPs, ",") != "192.168.1.163" || cur.Link.Kind != core.LinkUnknown {
		t.Errorf("running lease = %+v, want its address, not online, link unknown", cur)
	}
}

// The VM's local network is a bridge with one cable port; on a single-port
// local network there is no bridge, and the neighbour table is all there is.
func TestWithoutABridgeTheNeighbourTableDecides(t *testing.T) {
	fx := newDevFixture(t, mapSys{}, "")
	for _, r := range [][]byte{
		ndRecord(3, nudReachable, "192.168.1.20", "52:54:00:00:53:12"),
		ndRecord(3, nudStale, "192.168.1.21", "52:54:00:00:53:13"),
	} {
		n, _ := parseNeighMsg(r, 3)
		fx.neigh = append(fx.neigh, n)
	}
	got := fx.list()
	if d := got["52:54:00:00:53:12"]; !d.Online || d.Link.Kind != core.LinkCable {
		t.Errorf("reachable = %+v, want online on the cable", d)
	}
	if d := got["52:54:00:00:53:13"]; d.Online {
		t.Errorf("stale = %+v, want not online: STALE is kept for hours after a device leaves", d)
	}
}

func TestNoLocalNetworkIsSaidAsSuch(t *testing.T) {
	fx := newDevFixture(t, cudy(), "")
	fx.m.net.lookupInterfaces = func() ([]core.NetworkInterface, error) {
		return []core.NetworkInterface{{Name: "wan", Device: "eth0"}}, nil
	}
	if _, err := fx.m.ListDevices(); !errors.Is(err, core.ErrNoLAN) {
		t.Errorf("err = %v, want ErrNoLAN", err)
	}
}

func TestRememberedRecordIsBounded(t *testing.T) {
	m := &deviceManager{seen: map[string]heard{}}
	base := time.Unix(0, 0)
	for i := 0; i < maxRemembered+10; i++ {
		hw := net.HardwareAddr{0x02, 0, 0, byte(i >> 16), byte(i >> 8), byte(i)}
		m.remember(hw.String(), heard{at: base.Add(time.Duration(i) * time.Second)})
	}
	if len(m.seen) != maxRemembered {
		t.Fatalf("remembered %d, want %d", len(m.seen), maxRemembered)
	}
	if _, ok := m.seen["02:00:00:00:00:00"]; ok {
		t.Error("the longest-unheard entry was kept")
	}
}

// Captured on the VM (x86, little endian), hardware addresses replaced: a LAN
// client heard 112.73 s ago on port 1, and the bridge's own address flagged
// local. Every address in these fixtures is from a documentation range.
func TestForwardingTableIsReadAsTheKernelWritesIt(t *testing.T) {
	raw := []byte{
		0x52, 0x54, 0x00, 0x00, 0x53, 0x12, 0x01, 0x00, 0x09, 0x2c, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x52, 0x54, 0x00, 0x00, 0x53, 0x8e, 0x01, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	}
	if binary.NativeEndian.Uint16([]byte{1, 0}) != 1 {
		t.Skip("captured on a little-endian CPU")
	}
	got := parseForwarding(raw, map[int]string{1: "eth0"}, func(string) bool { return false }, nil)
	if len(got) != 2 || got[0].mac != "52:54:00:00:53:12" || got[0].local || got[0].age != 112730*time.Millisecond {
		t.Errorf("entry 0 = %+v, want 52:54:00:00:53:12 not local, 112.73 s", got)
	}
	if !got[1].local {
		t.Errorf("entry 1 = %+v, want the bridge's own address flagged local", got[1])
	}
}
