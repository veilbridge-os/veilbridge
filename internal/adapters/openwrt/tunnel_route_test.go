package openwrt

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/veilbridge-os/veilbridge/internal/config"
	"github.com/veilbridge-os/veilbridge/internal/core"
	"github.com/veilbridge-os/veilbridge/internal/routing"
	"github.com/veilbridge-os/veilbridge/internal/vpn"
)

func newTestRouter(v6 bool) (*tunnelRouter, *fakeRunner) {
	f := newFakeRunner()
	k := &fakeKernelRules{f: f, rules: map[string]map[string]string{}, refuse: map[string]bool{}}
	r := newTunnelRouter(k.run)
	r.hasIPv6 = func() bool { return v6 }
	return r, f
}

// fakeKernelRules keeps the rules and the refusing route the way the kernel
// would, on top of fakeRunner (which records calls and injects failures), so
// a rebuild can be verified like on a router.
type fakeKernelRules struct {
	f      *fakeRunner
	rules  map[string]map[string]string // family → priority → "from all …"
	refuse map[string]bool
}

func (k *fakeKernelRules) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := k.f.run(ctx, name, args...)
	if err != nil || len(args) < 3 {
		return out, err
	}
	fam, verb := args[0], args[1]+" "+args[2]
	if k.rules[fam] == nil {
		k.rules[fam] = map[string]string{}
	}
	line := strings.Join(args, " ")
	switch {
	case verb == "rule add":
		prio := args[len(args)-1]
		k.rules[fam][prio] = "from all " + strings.Join(args[3:len(args)-2], " ")
	case verb == "rule del":
		prio := args[len(args)-1]
		if _, ok := k.rules[fam][prio]; !ok {
			return nil, errors.New("RTNETLINK answers: No such file or directory")
		}
		delete(k.rules[fam], prio)
	case verb == "rule show" && len(out) == 0:
		prios := slices.Sorted(maps.Keys(k.rules[fam]))
		var b strings.Builder
		for _, p := range prios {
			fmt.Fprintf(&b, "%s:\t%s \n", p, k.rules[fam][p])
		}
		return []byte(b.String()), nil
	case strings.HasPrefix(line, fam+" route replace unreachable default table "+refuseTable):
		k.refuse[fam] = true
	case strings.HasPrefix(line, fam+" route show table "+refuseTable) && len(out) == 0:
		if k.refuse[fam] {
			return []byte("unreachable default\n"), nil
		}
	}
	return out, nil
}

// The rules are the recipe measured in D-107, in this order of priority: the
// engine's own socket bypasses the tunnel, marked traffic takes the whole
// table and is refused if it found nothing there, and everything else takes
// main, then the tunnel's table, each without its default.
func TestTunnelRouterEnsureRules(t *testing.T) {
	r, f := newTestRouter(true)
	if err := r.Ensure(); err != nil {
		t.Fatal(err)
	}
	for _, fam := range []string{"-4", "-6"} {
		for _, want := range []string{
			ipProgram + " " + fam + " rule add fwmark 0x20000000/0x20000000 lookup main priority 30090",
			ipProgram + " " + fam + " rule add fwmark 0x40000000/0x40000000 lookup 147 priority 30100",
			ipProgram + " " + fam + " rule add fwmark 0x40000000/0x40000000 lookup 148 priority 30101",
			ipProgram + " " + fam + " rule add lookup main suppress_prefixlength 0 priority 30102",
			ipProgram + " " + fam + " rule add lookup 147 suppress_prefixlength 0 priority 30103",
			ipProgram + " " + fam + " route replace unreachable default table 148",
		} {
			if !slices.Contains(f.calls, want) {
				t.Errorf("missing %q in:\n%s", want, strings.Join(f.calls, "\n"))
			}
		}
	}
	// Idempotent: every priority is cleared before it is added again.
	del := slices.Index(f.calls, ipProgram+" -4 rule del priority 30100")
	add := slices.Index(f.calls, ipProgram+" -4 rule add fwmark 0x40000000/0x40000000 lookup 147 priority 30100")
	if del < 0 || add < 0 || del > add {
		t.Errorf("rule 30100 must be deleted before it is added (del=%d add=%d)", del, add)
	}
}

// The deletion loop stops at the first refusal: "no such rule" is the normal
// end, and a runner that always fails must not spin. Here even the hold
// cannot come out, which Ensure must report.
func TestTunnelRouterEnsureDeleteLoopIsBounded(t *testing.T) {
	r, f := newTestRouter(false)
	f.fail[ipProgram+" -4 rule del"] = errors.New("RTNETLINK answers: Operation not permitted")
	if err := r.Ensure(); err == nil {
		t.Fatal("a hold that cannot be removed must be reported")
	}
	n := 0
	for _, c := range f.calls {
		if strings.Contains(c, "rule del") {
			n++
		}
	}
	// One failed delete per priority (5), plus the hold cleared after the
	// rebuild verified (1); an existing hold is never deleted first.
	if n != 6 {
		t.Errorf("want 6 bounded deletes, got %d", n)
	}
}

const rulesInPlace = `0:	from all lookup local
30090:	from all fwmark 0x20000000/0x20000000 lookup main 
30100:	from all fwmark 0x40000000/0x40000000 lookup 147 
30101:	from all fwmark 0x40000000/0x40000000 lookup 148 
30102:	from all lookup main suppress_prefixlength 0 
30103:	from all lookup 147 suppress_prefixlength 0 
32766:	from all lookup main
32767:	from all lookup default
`

// Rules already in place are not touched: switching or restoring a node never
// takes the refusal away, not even for a moment.
func TestTunnelRouterEnsureLeavesRulesInPlace(t *testing.T) {
	r, f := newTestRouter(true)
	for _, fam := range []string{"-4", "-6"} {
		f.out[ipProgram+" "+fam+" rule show"] = []byte(rulesInPlace)
		f.out[ipProgram+" "+fam+" route show table 148"] = []byte("unreachable default \n")
	}
	if err := r.Ensure(); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls {
		if strings.Contains(c, "rule add") || strings.Contains(c, "rule del") {
			t.Errorf("rules in place were rewritten: %q", c)
		}
	}
}

// newSeededRouter is a router over a kernel that already holds the given
// IPv4 rules (priority → text), as after somebody else's change.
func newSeededRouter(seed map[string]string) (*tunnelRouter, *fakeRunner, *fakeKernelRules) {
	f := newFakeRunner()
	k := &fakeKernelRules{f: f, rules: map[string]map[string]string{"-4": seed}, refuse: map[string]bool{}}
	r := newTunnelRouter(k.run)
	r.hasIPv6 = func() bool { return false }
	return r, f, k
}

// A rebuild keeps marked traffic refused throughout: the hold goes in before
// the first rule is deleted and comes out only after the new set verifies.
func TestTunnelRouterRebuildHoldsMarkedTraffic(t *testing.T) {
	r, f, k := newSeededRouter(map[string]string{"30101": "from all lookup 999"})
	if err := r.Ensure(); err != nil {
		t.Fatal(err)
	}
	hold := slices.Index(f.calls, ipProgram+" -4 rule add fwmark 0x40000000/0x40000000 lookup 148 priority 30089")
	firstDel := slices.Index(f.calls, ipProgram+" -4 rule del priority 30090")
	lastAdd := slices.Index(f.calls, ipProgram+" -4 rule add lookup 147 suppress_prefixlength 0 priority 30103")
	if hold < 0 || firstDel < 0 || lastAdd < 0 || hold > firstDel {
		t.Errorf("hold=%d firstDel=%d lastAdd=%d:\n%s", hold, firstDel, lastAdd, strings.Join(f.calls, "\n"))
	}
	if _, held := k.rules["-4"][prioHold]; held {
		t.Error("the hold must come out once the rules verify")
	}
	if k.rules["-4"][prioRefuse] != "from all fwmark 0x40000000/0x40000000 lookup 148" {
		t.Errorf("somebody else's rule was not replaced: %q", k.rules["-4"][prioRefuse])
	}
}

// Without the hold nothing is torn down: the old rules keep refusing.
func TestTunnelRouterRebuildNeedsTheHold(t *testing.T) {
	// Only the refusal is in place; the rest is missing, so a rebuild is due.
	r, f, k := newSeededRouter(map[string]string{prioRefuse: "from all fwmark 0x40000000/0x40000000 lookup 148"})
	f.fail[ipProgram+" -4 rule add fwmark 0x40000000/0x40000000 lookup 148 priority 30089"] = errors.New("boom")
	if err := r.Ensure(); err == nil {
		t.Fatal("a rebuild without its hold must fail")
	}
	if _, ok := k.rules["-4"][prioRefuse]; !ok {
		t.Error("the existing refusal was deleted without a hold")
	}
}

// If the new refusal cannot be added, the hold stays: refuse, do not leak.
func TestTunnelRouterRebuildKeepsTheHoldOnFailure(t *testing.T) {
	r, f, k := newSeededRouter(map[string]string{})
	f.fail[ipProgram+" -4 rule add fwmark 0x40000000/0x40000000 lookup 148 priority 30101"] = errors.New("boom")
	if err := r.Ensure(); err == nil {
		t.Fatal("Ensure must report the missing refusal")
	}
	if _, held := k.rules["-4"][prioHold]; !held {
		t.Error("the hold was removed although the refusal is missing")
	}
}

// An exit (everything allowed) gets a default route per family; a VPN to work
// gets its networks only, so marked traffic elsewhere is refused instead of
// leaving through it.
func TestTunnelRouterUpExitAndWorkNetwork(t *testing.T) {
	r, f := newTestRouter(true)
	if err := r.Up("awg0", []string{"0.0.0.0/0", "::/0", "198.51.100.7/24"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		ipProgram + " -4 route flush table 147",
		ipProgram + " -6 route flush table 147",
		ipProgram + " -4 route replace default dev awg0 table 147",
		ipProgram + " -6 route replace default dev awg0 table 147",
		// Normalised to the network, as the kernel would.
		ipProgram + " -4 route replace 198.51.100.0/24 dev awg0 table 147",
	} {
		if !slices.Contains(f.calls, want) {
			t.Errorf("missing %q in:\n%s", want, strings.Join(f.calls, "\n"))
		}
	}

	r, f = newTestRouter(true)
	if err := r.Up("awg0", []string{"198.51.100.0/24", "2001:db8:48::/64"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls {
		if strings.Contains(c, "route replace default dev") {
			t.Errorf("a connection to some networks must not get a default route: %q", c)
		}
	}
	if !slices.Contains(f.calls, ipProgram+" -6 route replace 2001:db8:48::/64 dev awg0 table 147") {
		t.Errorf("IPv6 network not routed:\n%s", strings.Join(f.calls, "\n"))
	}
}

// No AllowedIPs means everything, the same default the engine applies.
func TestTunnelRouterUpEmptyAllowedIsExit(t *testing.T) {
	r, f := newTestRouter(true)
	if err := r.Up("awg0", nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.calls, ipProgram+" -4 route replace default dev awg0 table 147") ||
		!slices.Contains(f.calls, ipProgram+" -6 route replace default dev awg0 table 147") {
		t.Errorf("empty AllowedIPs must route everything:\n%s", strings.Join(f.calls, "\n"))
	}
}

// Without IPv6 in the kernel the IPv6 half is skipped, not failed.
func TestTunnelRouterWithoutIPv6(t *testing.T) {
	r, f := newTestRouter(false)
	f.fail[ipProgram+" -6"] = errors.New("ip -6 must not be called")
	if err := r.Up("awg0", []string{"0.0.0.0/0", "::/0"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, ipProgram+" -6") {
			t.Errorf("IPv6 command without IPv6: %q", c)
		}
	}
}

func TestTunnelRouterUpReportsFailures(t *testing.T) {
	r, f := newTestRouter(false)
	f.fail[ipProgram+" -4 route replace 198.51.100.0/24"] = errors.New("boom")
	if err := r.Up("awg0", []string{"198.51.100.0/24"}); err == nil {
		t.Fatal("a route that could not be added must fail Up")
	}
	if err := r.Up("awg0", []string{"not-a-prefix"}); err == nil {
		t.Fatal("a malformed network must fail Up")
	}
}

// The router's own networks win over a connection's networks for unmarked
// traffic: main without its default is consulted before the tunnel's table.
func TestTunnelRouterLocalNetworksComeFirst(t *testing.T) {
	r, f := newTestRouter(false)
	if err := r.Ensure(); err != nil {
		t.Fatal(err)
	}
	local := slices.Index(f.calls, ipProgram+" -4 rule add lookup main suppress_prefixlength 0 priority 30102")
	nets := slices.Index(f.calls, ipProgram+" -4 rule add lookup 147 suppress_prefixlength 0 priority 30103")
	if local < 0 || nets < 0 {
		t.Fatalf("rules missing:\n%s", strings.Join(f.calls, "\n"))
	}
	if local > nets {
		t.Errorf("main without its default must be added before the tunnel's table")
	}
}

// Everything in two halves is an exit, not a takeover of all LAN traffic.
func TestTunnelRouterHalvesAreTheDefaultRoute(t *testing.T) {
	r, f := newTestRouter(true)
	if err := r.Up("awg0", []string{"0.0.0.0/1", "128.0.0.0/1", "::/1", "8000::/1", "198.51.100.0/24"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		ipProgram + " -4 route replace default dev awg0 table 147",
		ipProgram + " -6 route replace default dev awg0 table 147",
		ipProgram + " -4 route replace 198.51.100.0/24 dev awg0 table 147",
	} {
		if !slices.Contains(f.calls, want) {
			t.Errorf("missing %q in:\n%s", want, strings.Join(f.calls, "\n"))
		}
	}
	for _, c := range f.calls {
		if strings.Contains(c, "0.0.0.0/1") || strings.Contains(c, "128.0.0.0/1") || strings.Contains(c, "8000::/1") {
			t.Errorf("a half was routed as is: %q", c)
		}
	}
	// One half alone stays a network.
	r, f = newTestRouter(false)
	if err := r.Up("awg0", []string{"0.0.0.0/1"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.calls, ipProgram+" -4 route replace 0.0.0.0/1 dev awg0 table 147") {
		t.Errorf("a single half must be routed as a network:\n%s", strings.Join(f.calls, "\n"))
	}
}

// fakeRouter records what vpnManager asks of the router.
type fakeRouter struct {
	ensured int
	upDev   string
	allowed []string
	downs   int
	upErr   error
}

func (r *fakeRouter) Ensure() error { r.ensured++; return nil }
func (r *fakeRouter) Up(dev string, allowed []string) error {
	if r.upErr != nil {
		return r.upErr
	}
	r.upDev, r.allowed = dev, allowed
	return nil
}
func (r *fakeRouter) Down() error { r.downs++; return nil }

func activateSample(t *testing.T, m *vpnManager) string {
	t.Helper()
	nodes, err := m.ImportConfig([]byte(sampleConf))
	if err != nil {
		t.Fatal(err)
	}
	return nodes[0].ID
}

func TestActivateRoutesTheTunnelInterface(t *testing.T) {
	eng := &fakeKernelEngine{}
	m, _ := newTestVPN(t, eng)
	rt := &fakeRouter{}
	m.router = rt
	id := activateSample(t, m)
	if err := m.Activate(id); err != nil {
		t.Fatal(err)
	}
	if rt.upDev != "awg0" || !slices.Equal(rt.allowed, []string{"0.0.0.0/0"}) {
		t.Errorf("router.Up(%q, %v), want awg0 with the node's AllowedIPs", rt.upDev, rt.allowed)
	}
	if err := m.RemoveNode(id); err != nil {
		t.Fatal(err)
	}
	if rt.downs == 0 || eng.up {
		t.Errorf("removing the active node must empty the table and stop the engine (downs=%d up=%v)", rt.downs, eng.up)
	}
}

// A tunnel nothing routes into is what #47 was; it must not be reported up.
func TestActivateFailsWhenRoutingFails(t *testing.T) {
	eng := &fakeKernelEngine{}
	m, store := newTestVPN(t, eng)
	m.router = &fakeRouter{upErr: errors.New("boom")}
	id := activateSample(t, m)
	if err := m.Activate(id); err == nil {
		t.Fatal("Activate must fail when the tunnel cannot be routed")
	}
	if eng.up || m.up != "" {
		t.Errorf("engine left up after a routing failure (up=%v m.up=%q)", eng.up, m.up)
	}
	doc, _ := store.Load()
	if doc.Settings.ActiveNodeID != "" {
		t.Errorf("a node that failed to route must not be saved as active")
	}
}

// A userspace engine has no interface: nothing to route, nothing to fail.
func TestActivateUserspaceEngineDoesNotRoute(t *testing.T) {
	m, _ := newTestVPN(t, &fakeEngine{})
	rt := &fakeRouter{}
	m.router = rt
	if err := m.Activate(activateSample(t, m)); err != nil {
		t.Fatal(err)
	}
	if rt.upDev != "" {
		t.Errorf("userspace engine routed: %q", rt.upDev)
	}
}

// After a restart the rules come back and the node that was active comes up
// again; before, it stayed down until somebody activated it by hand (D-107).
func TestRestoreBringsBackTheActiveNode(t *testing.T) {
	eng := &fakeKernelEngine{}
	m, store := newTestVPN(t, eng)
	id := activateSample(t, m)
	doc, _ := store.Load()
	doc.Settings.ActiveNodeID = id
	if err := store.Save(doc); err != nil {
		t.Fatal(err)
	}

	// A fresh manager over the same store: what the daemon sees after a restart.
	m2 := newVPNManager(store, eng)
	rt := &fakeRouter{}
	m2.router = rt
	if err := m2.Restore(); err != nil {
		t.Fatal(err)
	}
	waitUp(t, m2, id)
	if rt.ensured == 0 || !eng.up || rt.upDev != "awg0" {
		t.Errorf("restore: ensured=%d engine up=%v dev=%q", rt.ensured, eng.up, rt.upDev)
	}
}

func waitUp(t *testing.T, m *vpnManager, id string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		up := m.up
		m.mu.Unlock()
		if up == id {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("node %s did not come back up", id)
}

// flakyKernelEngine fails Up a given number of times, as when the server's
// name does not resolve yet at boot.
type flakyKernelEngine struct {
	fakeKernelEngine
	mu    sync.Mutex
	fails int
}

func (f *flakyKernelEngine) Up(cfg vpn.NodeConfig) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fails > 0 {
		f.fails--
		return errors.New("lookup server: no such host")
	}
	return f.fakeKernelEngine.Up(cfg)
}

// A node that cannot come up at start is retried in the background until it
// does; Restore itself does not wait for it.
func TestRestoreRetriesUntilTheNodeComesUp(t *testing.T) {
	eng := &flakyKernelEngine{}
	m, store := newTestVPN(t, eng)
	id := activateSample(t, m)
	doc, _ := store.Load()
	doc.Settings.ActiveNodeID = id
	if err := store.Save(doc); err != nil {
		t.Fatal(err)
	}
	eng.fails = 3
	m2 := newVPNManager(store, eng)
	m2.router = &fakeRouter{}
	m2.retry = 10 * time.Millisecond
	if err := m2.Restore(); err != nil {
		t.Fatal(err)
	}
	waitUp(t, m2, id)
}

// Retrying stops once somebody chose otherwise: the node was removed.
func TestRestoreRetryStopsWhenTheNodeIsRemoved(t *testing.T) {
	eng := &flakyKernelEngine{fails: 1 << 30}
	m, store := newTestVPN(t, eng)
	m.retry = 20 * time.Millisecond
	id := activateSample(t, m)
	doc, _ := store.Load()
	doc.Settings.ActiveNodeID = id
	if err := store.Save(doc); err != nil {
		t.Fatal(err)
	}
	if err := m.Restore(); err != nil {
		t.Fatal(err)
	}
	if err := m.RemoveNode(id); err != nil {
		t.Fatal(err)
	}
	// A node that never came up is no longer the one to bring back.
	if doc, _ := store.Load(); doc.Settings.ActiveNodeID != "" {
		t.Errorf("removed node still active: %q", doc.Settings.ActiveNodeID)
	}
	time.Sleep(100 * time.Millisecond)
	eng.mu.Lock()
	before := eng.fails
	eng.mu.Unlock()
	time.Sleep(100 * time.Millisecond)
	eng.mu.Lock()
	after := eng.fails
	eng.mu.Unlock()
	if before != after {
		t.Errorf("still retrying a node nobody wants: %d attempts in 100 ms", before-after)
	}
}

// With no active node the rules still go in: traffic marked for the tunnel is
// refused instead of leaving by the uplink.
func TestRestoreWithoutActiveNodeStillEnsuresRules(t *testing.T) {
	eng := &fakeKernelEngine{}
	store := config.NewStore(t.TempDir() + "/config.json")
	m := newVPNManager(store, eng)
	rt := &fakeRouter{}
	m.router = rt
	if err := m.Restore(); err != nil {
		t.Fatal(err)
	}
	if rt.ensured != 1 || eng.up {
		t.Errorf("ensured=%d engine up=%v", rt.ensured, eng.up)
	}
}

// Marked traffic must be refused before main is consulted: otherwise, with
// the connection down, an address in the router's own networks (its WAN
// subnet, a static route) took main's route and left directly (measured).
func TestTunnelRouterRefusalComesBeforeMain(t *testing.T) {
	prio := func(name string) string {
		switch name {
		case "refuse":
			return prioRefuse
		case "local":
			return prioLocal
		case "marked":
			return prioMarked
		}
		return ""
	}
	if !(prio("marked") < prio("refuse") && prio("refuse") < prio("local")) {
		t.Errorf("priorities: marked %s, refuse %s, local %s", prio("marked"), prio("refuse"), prio("local"))
	}
}

// The lan zone's interfaces: its networks resolved to their devices, plus a
// device listed on the zone itself; other zones are not included.
func TestLANDevicesFromTheLanZone(t *testing.T) {
	f := newFakeRunner()
	f.out["uci -q -c"] = []byte(`firewall.@zone[0]=zone
firewall.@zone[0].name='lan'
firewall.@zone[0].network='lan' 'iot'
firewall.@zone[0].device='tap0'
firewall.@zone[1]=zone
firewall.@zone[1].name='guest'
firewall.@zone[1].network='guest'
`)
	m := networkManager{run: f.run, lookupInterfaces: func() ([]core.NetworkInterface, error) {
		return []core.NetworkInterface{
			{Name: "lan", Device: "br-lan"},
			{Name: "iot", Device: "br-iot"},
			{Name: "guest", Device: "br-guest"},
		}, nil
	}}
	got, err := m.lanDevices()
	if err != nil {
		t.Fatal(err)
	}
	// The committed view, not the one a staged draft changes.
	if f.sawPrefix("uci -q show firewall") || !f.sawPrefix("uci -q -c ") {
		t.Errorf("lanDevices must read the committed firewall: %v", f.calls)
	}
	if want := []string{"br-iot", "br-lan", "tap0"}; !slices.Equal(got, want) {
		t.Errorf("lanDevices() = %v, want %v", got, want)
	}

	f.out["uci -q -c"] = []byte("firewall.@zone[0]=zone\nfirewall.@zone[0].name='wan'\n")
	if _, err := m.lanDevices(); err == nil {
		t.Error("no lan zone must be an error")
	}
}

// A node the person activates while a restore is still retrying wins: the
// retry stops instead of tearing it down to bring back the old one.
func TestRestoreDoesNotOverrideANewerChoice(t *testing.T) {
	eng := &flakyKernelEngine{fails: 1 << 30}
	m, store := newTestVPN(t, eng)
	m.retry = 10 * time.Millisecond
	m.router = &fakeRouter{}
	old := activateSample(t, m)
	doc, _ := store.Load()
	doc.Settings.ActiveNodeID = old
	if err := store.Save(doc); err != nil {
		t.Fatal(err)
	}
	if err := m.Restore(); err != nil {
		t.Fatal(err)
	}
	// The person picks another node; the engine works again.
	other := strings.Replace(sampleConf,
		"c2VydmVycHVibGlja2V5ZXhhbXBsZWJhc2U2NHN0cmluZ2hlcmU=",
		"b3RoZXJzZXJ2ZXJwdWJsaWNrZXlleGFtcGxlYmFzZTY0c3RyaW5n", 1)
	nodes, err := m.ImportConfig([]byte(other))
	if err != nil {
		t.Fatal(err)
	}
	eng.mu.Lock()
	eng.fails = 0
	eng.mu.Unlock()
	if err := m.Activate(nodes[0].ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	m.mu.Lock()
	up := m.up
	m.mu.Unlock()
	if up != nodes[0].ID {
		t.Errorf("restore replaced the newer choice: up=%q want %q", up, nodes[0].ID)
	}
}

// Marking without the tunnel's rules sends marked traffic out by the uplink;
// Apply marks nothing unless the rules are in place.
func TestRoutingApplyNeedsTheTunnelRules(t *testing.T) {
	store := config.NewStore(t.TempDir() + "/config.json")
	r := newRoutingManager(store)
	r.lan = func() ([]string, error) {
		t.Fatal("marking was prepared although the tunnel's rules failed")
		return nil, nil
	}
	r.ensure = func() error { return errors.New("RTNETLINK answers: Operation not permitted") }
	err := r.Apply()
	if err == nil || !strings.Contains(err.Error(), "nothing is marked") {
		t.Fatalf("Apply() = %v, want a refusal to mark", err)
	}
}

// An interrupted rebuild left the hold as the only refusal: the next rebuild
// keeps it until the new rules verify, never deleting it first.
func TestTunnelRouterRebuildKeepsAnExistingHold(t *testing.T) {
	r, f, k := newSeededRouter(map[string]string{prioHold: "from all fwmark 0x40000000/0x40000000 lookup 148"})
	if err := r.Ensure(); err != nil {
		t.Fatal(err)
	}
	firstHoldDel := slices.Index(f.calls, ipProgram+" -4 rule del priority 30089")
	lastAdd := slices.Index(f.calls, ipProgram+" -4 rule add lookup 147 suppress_prefixlength 0 priority 30103")
	if firstHoldDel >= 0 && firstHoldDel < lastAdd {
		t.Errorf("the existing hold was deleted before the rules were rebuilt:\n%s", strings.Join(f.calls, "\n"))
	}
	if _, held := k.rules["-4"][prioHold]; held {
		t.Error("the hold must come out once the rules verify")
	}
}

// Rules in place with a hold left over: the hold goes, or marked traffic
// would stay refused while the tunnel reads as up.
func TestTunnelRouterEnsureClearsAStaleHold(t *testing.T) {
	r, _, k := newSeededRouter(map[string]string{})
	if err := r.Ensure(); err != nil {
		t.Fatal(err)
	}
	k.rules["-4"][prioHold] = "from all fwmark 0x40000000/0x40000000 lookup 148"
	if err := r.Ensure(); err != nil {
		t.Fatal(err)
	}
	if _, held := k.rules["-4"][prioHold]; held {
		t.Error("a stale hold survived although the rules are in place")
	}
}

// SO_MARK takes the mark as a number; it must be the generator's mark.
func TestTunnelMarkValueMatchesTheGenerator(t *testing.T) {
	if got := fmt.Sprintf("0x%x", tunnelMarkValue); got != routing.TunnelMark {
		t.Errorf("tunnelMarkValue %s != routing.TunnelMark %s", got, routing.TunnelMark)
	}
}

// A failed apply at start is retried until it succeeds, then stops.
func TestRoutingApplyUntilDoneRetries(t *testing.T) {
	r := newRoutingManager(config.NewStore(t.TempDir() + "/config.json"))
	r.retry = time.Millisecond
	calls := 0
	r.attempt = func() error {
		calls++
		if calls < 4 {
			return errors.New("not yet")
		}
		return nil
	}
	r.applyUntilDone()
	if calls != 4 {
		t.Errorf("want 4 attempts (3 failures, 1 success), got %d", calls)
	}
}

// A hold that cannot be removed is an error, not a success: otherwise the
// tunnel reads as up while marked traffic stays refused.
func TestTunnelRouterReleaseFailureIsReported(t *testing.T) {
	r, f, k := newSeededRouter(map[string]string{})
	if err := r.Ensure(); err != nil {
		t.Fatal(err)
	}
	k.rules["-4"][prioHold] = "from all fwmark 0x40000000/0x40000000 lookup 148"
	f.fail[ipProgram+" -4 rule del priority 30089"] = errors.New("RTNETLINK answers: Operation not permitted")
	if err := r.Ensure(); err == nil {
		t.Fatal("a hold that stays must fail Ensure")
	}
}

// Activation saves only the choice: something saved meanwhile survives.
func TestActivateKeepsConcurrentEdits(t *testing.T) {
	eng := &fakeKernelEngine{}
	m, store := newTestVPN(t, eng)
	id := activateSample(t, m)
	m.router = &fakeRouterHook{onUp: func() {
		// Another manager saves while the tunnel is being routed.
		_ = store.Update(func(d *config.Document) error {
			d.Routes = append(d.Routes, core.RouteRule{ID: "r1", Kind: core.RuleSubnet, Value: "198.51.100.0/24", Target: core.TargetTunnel})
			return nil
		})
	}}
	if err := m.Activate(id); err != nil {
		t.Fatal(err)
	}
	doc, _ := store.Load()
	if len(doc.Routes) != 1 || doc.Settings.ActiveNodeID != id {
		t.Errorf("routes=%v active=%q: the activation overwrote a concurrent edit", doc.Routes, doc.Settings.ActiveNodeID)
	}
}

type fakeRouterHook struct {
	fakeRouter
	onUp func()
}

func (r *fakeRouterHook) Up(dev string, allowed []string) error {
	r.onUp()
	return r.fakeRouter.Up(dev, allowed)
}
