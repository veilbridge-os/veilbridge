package openwrt

import (
	"strings"
	"testing"

	"github.com/veilbridge-os/veilbridge/internal/core"
)

// #56. The reading below has the shape `nft -j list table` printed on the
// router (25.12.5, 29.09), with documentation addresses.
const trafficJSON = `{"nftables": [{"metainfo": {"version": "1.1.6"}},
 {"table": {"family": "inet", "name": "veilbridge_traffic", "handle": 5, "comment": "vb uptime=40 dev=br-lan"}},
 {"set": {"family": "inet", "name": "up", "table": "veilbridge_traffic", "type": "ether_addr", "handle": 1, "size": 4096, "flags": ["dynamic"], "stmt": [{"counter": null}],
   "elem": [{"elem": {"val": "02:00:5e:00:53:d6", "counter": {"packets": 147, "bytes": 1241963}}}]}},
 {"set": {"family": "inet", "name": "down4", "table": "veilbridge_traffic", "type": "ipv4_addr", "handle": 2, "size": 8192, "flags": ["dynamic"],
   "elem": [{"elem": {"val": "192.0.2.137", "counter": {"packets": 14000, "bytes": 20756122}}}]}},
 {"set": {"family": "inet", "name": "down6", "table": "veilbridge_traffic", "type": "ipv6_addr", "handle": 3, "size": 8192, "flags": ["dynamic"],
   "elem": [{"elem": {"val": "2001:db8:0:c:eb:733b:904e:e0e2", "counter": {"packets": 14100, "bytes": 20775888}}},
            {"elem": {"val": "2001:db8:0:c::99", "counter": {"packets": 3, "bytes": 500}}}]}}]}`

func TestTheCountingTableIsReadAsTheRouterPrintsIt(t *testing.T) {
	r, ok := parseTraffic([]byte(trafficJSON))
	if !ok || r.startedAt != 40 || r.dev != "br-lan" {
		t.Fatalf("read = %+v ok=%v", r, ok)
	}
	if r.up["02:00:5e:00:53:d6"] != 1241963 || r.down["192.0.2.137"] != 20756122 ||
		r.down["2001:db8:0:c:eb:733b:904e:e0e2"] != 20775888 {
		t.Errorf("read = %+v", r)
	}
	if _, ok := parseTraffic([]byte(`{"nftables": [{"table": {"name": "veilbridge_traffic", "comment": "vb uptime=40 dev=br-lan"}}]}`)); ok {
		t.Error("a table without its sets reads as fine: it would never be made again")
	}
}

func TestTheRulesetCountsOnlyWhatPassedAndOnTheLocalNetwork(t *testing.T) {
	rs := trafficRuleset("br-lan", 40)
	for _, want := range []string{
		"table inet veilbridge_traffic", `comment "vb uptime=40 dev=br-lan"`,
		"hook forward priority filter + 10", "policy accept",
		`iifname "br-lan" update @up { ether saddr }`,
		`oifname "br-lan" meta nfproto ipv4 update @down4 { ip daddr }`,
		`oifname "br-lan" meta nfproto ipv6 update @down6 { ip6 daddr }`,
	} {
		if !strings.Contains(rs, want) {
			t.Errorf("ruleset lacks %q:\n%s", want, rs)
		}
	}
	if strings.Contains(rs, "drop") || strings.Contains(rs, "reject") || strings.Contains(rs, "fw4") {
		t.Error("the counting table decides something or touches fw4's")
	}
}

// Received bytes reach a device through the addresses it is known by, the
// router's reading goes into the list, and "since the router started" is said
// only when counting started with it.
func TestTheListCarriesEachDevicesTraffic(t *testing.T) {
	sys := cudy()
	sys.files["/proc/uptime"] = []byte("7240.55 12000.10\n")
	fx := newDevFixture(t, sys, "")
	fx.runner.out[nftProgram+" -j list table inet veilbridge_traffic"] = []byte(trafficJSON)
	fx.neigh = []neighbour{
		{mac: "02:00:5e:00:53:d6", ip: "192.0.2.137"},
		{mac: "02:00:5e:00:53:d6", ip: "2001:db8:0:c:eb:733b:904e:e0e2"},
	}
	l, err := fx.m.ListDevices()
	if err != nil {
		t.Fatal(err)
	}
	if l.Traffic == nil || l.Traffic.SinceSec != 7200 || !l.Traffic.SinceBoot || l.Traffic.Partial {
		t.Fatalf("traffic = %+v", l.Traffic)
	}
	var d core.Device
	for _, x := range l.Devices {
		if x.MAC == "02:00:5e:00:53:d6" {
			d = x
		}
	}
	if d.RxBytes != 20756122+20775888 || d.TxBytes != 1241963 {
		t.Errorf("phone rx=%d tx=%d", d.RxBytes, d.TxBytes)
	}
	// The IPv6 address rotates away: its bytes stay with the phone.
	fx.neigh = []neighbour{{mac: "02:00:5e:00:53:d6", ip: "192.0.2.137"}}
	l, _ = fx.m.ListDevices()
	for _, x := range l.Devices {
		if x.MAC == "02:00:5e:00:53:d6" && x.RxBytes != 20756122+20775888 {
			t.Errorf("after the address changed rx=%d: the old address's bytes were lost", x.RxBytes)
		}
	}
}

func TestAMissingTableIsMadeAndCountingSaysWhenItStarted(t *testing.T) {
	sys := cudy()
	sys.files["/proc/uptime"] = []byte("90000.00 1.0\n")
	fx := newDevFixture(t, sys, "")
	fx.m.stateDir = t.TempDir()
	fx.runner.fail[nftProgram+" -j list table"] = errNoSuchTable
	fx.runner.out["uci -q get firewall.@defaults[0].flow_offloading"] = []byte("1\n")
	l, err := fx.m.ListDevices()
	if err != nil {
		t.Fatal(err)
	}
	if !fx.runner.sawPrefix(nftProgram + " -f ") {
		t.Fatal("the counting table was not put in place")
	}
	if l.Traffic == nil || l.Traffic.SinceSec != 0 || l.Traffic.SinceBoot || !l.Traffic.Partial {
		t.Errorf("traffic = %+v, want counting from now, not since boot, partial (offloading on)", l.Traffic)
	}
}

var errNoSuchTable = &nftErr{"Error: No such file or directory; list table inet veilbridge_traffic"}

type nftErr struct{ s string }

func (e *nftErr) Error() string { return e.s }
