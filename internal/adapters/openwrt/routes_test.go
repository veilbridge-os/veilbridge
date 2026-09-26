package openwrt

import (
	"context"
	"encoding/binary"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/veilbridge-os/veilbridge/internal/core"
)

// The shapes below are the ones the stands print (23.05.5 and 25.12.5 gave the
// same), with the lab's addresses swapped for documentation ranges.

// routesShow is `uci show network` with the three forms a route is found in:
// CIDR in target (the panel's and new LuCI's), target + netmask (older LuCI),
// and a route with an option the panel does not model.
const routesShow = `network.loopback=interface
network.loopback.device='lo'
network.loopback.proto='static'
network.lan=interface
network.lan.device='br-lan'
network.lan.proto='static'
network.lan.ipaddr='192.168.1.1/24'
network.wan=interface
network.wan.device='wan'
network.wan.proto='dhcp'
network.@route[0]=route
network.@route[0].interface='lan'
network.@route[0].target='10.20.0.0/16'
network.@route[0].gateway='192.168.1.2'
network.@route[0].name='Office'
network.@route[1]=route
network.@route[1].interface='lan'
network.@route[1].target='172.16.5.0'
network.@route[1].netmask='255.255.255.0'
network.@route[1].gateway='192.168.1.3'
network.@route[1].metric='10'
network.@route[2]=route
network.@route[2].interface='lan'
network.@route[2].target='172.16.9.0/24'
network.@route[2].gateway='192.168.1.4'
network.@route[2].table='100'
network.@route[3]=route
network.@route[3].interface='lan'
network.@route[3].target='172.16.7.0/24'
network.@route[3].gateway='192.168.1.5'
network.@route[3].disabled='1'
`

// procRoutes is /proc/net/route as x86-64 and aarch64 print it: the default
// route and the connected networks, route 0 as netifd applied it, and route 1
// with its metric.
const procRoutesHeader = "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n"

var procRoutes = procRoutesHeader +
	"wan\t00000000\t016433C6\t0003\t0\t0\t0\t00000000\t0\t0\t0\n" +
	"wan\t006433C6\t00000000\t0001\t0\t0\t0\t00FFFFFF\t0\t0\t0\n" +
	"br-lan\t0001A8C0\t00000000\t0001\t0\t0\t0\t00FFFFFF\t0\t0\t0\n" +
	"br-lan\t0000140A\t0201A8C0\t0003\t0\t0\t0\t0000FFFF\t0\t0\t0\n" +
	"br-lan\t000510AC\t0301A8C0\t0003\t0\t0\t10\t00FFFFFF\t0\t0\t0\n"

var routeIfaces = []core.NetworkInterface{
	{Name: "loopback", Device: "lo", Up: true, IPv4: []string{"127.0.0.1/8"}},
	{Name: "lan", Device: "br-lan", Up: true, IPv4: []string{"192.168.1.1/24"}},
	{Name: "wan", Device: "wan", Up: true, IPv4: []string{"198.51.100.42/24"}},
}

// routeRunner answers `uci show network` (with the draft) and its committed
// twin from fixtures, and hands everything else to the recording fake.
type routeRunner struct {
	uci       *recordingRunner
	show      string
	committed string
}

func (r *routeRunner) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	joined := strings.Join(args, " ")
	if name == "uci" && joined == "-q show network" {
		r.uci.calls = append(r.uci.calls, append([]string{name}, args...))
		return []byte(r.show), nil
	}
	if name == "uci" && len(args) == 5 && args[1] == "-c" && args[3] == "show" &&
		args[4] == committedPrefix+"network" {
		committed := r.committed
		if committed == "" {
			committed = r.show
		}
		return []byte(strings.ReplaceAll(committed, "network.", committedPrefix+"network.")), nil
	}
	return r.uci.run(ctx, name, args...)
}

func (r *routeRunner) called(prefix string) bool {
	for _, c := range r.uci.calls {
		if strings.HasPrefix(strings.Join(c, " "), prefix) {
			return true
		}
	}
	return false
}

func routeManager(t *testing.T, show, kernel string) (networkManager, *routeRunner) {
	t.Helper()
	values := map[string]string{}
	for _, line := range strings.Split(show, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			values[k] = unquoteUCI(v)
		}
	}
	r := &routeRunner{uci: &recordingRunner{values: values}, show: show}
	return networkManager{
		run:              r.run,
		configDir:        t.TempDir(),
		lookupInterfaces: func() ([]core.NetworkInterface, error) { return routeIfaces, nil },
		procFile: func(path string) ([]byte, error) {
			if path == procRoute4 {
				return []byte(kernel), nil
			}
			return nil, errors.New("no such file")
		},
	}, r
}

func TestProcRouteIsReadInTheCPUsByteOrder(t *testing.T) {
	routes := parseProcRoute(procRoutes)
	got := map[string]string{}
	for _, r := range routes {
		got[r.prefix.String()] = addrString(r.gateway) + "@" + r.device
	}
	want := map[string]string{
		"0.0.0.0/0":       "198.51.100.1@wan",
		"198.51.100.0/24": "@wan",
		"192.168.1.0/24":  "@br-lan",
		"10.20.0.0/16":    "192.168.1.2@br-lan",
		"172.16.5.0/24":   "192.168.1.3@br-lan",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
	if routes[4].metric != 10 {
		t.Errorf("metric = %d, want 10", routes[4].metric)
	}
	// A big-endian MIPS router prints the same address the other way round.
	if a, _ := procAddr4In("C0A80100", binary.BigEndian); a != netip.MustParseAddr("192.168.1.0") {
		t.Errorf("big-endian read = %v", a)
	}
	if a, _ := procAddr4In("0001A8C0", binary.LittleEndian); a != netip.MustParseAddr("192.168.1.0") {
		t.Errorf("little-endian read = %v", a)
	}
}

func TestProcRouteSkipsRoutesThatAreNotUp(t *testing.T) {
	down := procRoutesHeader + "br-lan\t0000140A\t0201A8C0\t0002\t0\t0\t0\t0000FFFF\t0\t0\t0\n"
	if got := parseProcRoute(down); len(got) != 0 {
		t.Errorf("a route without RTF_UP was read: %+v", got)
	}
}

func TestProcIPv6RouteIsRead(t *testing.T) {
	raw := "20010db8000000000000000000000000 20 00000000000000000000000000000000 00 fe800000000000000000000000000001 00000400 00000000 00000000 00000003     br-lan\n"
	got := parseProcRoute6(raw)
	if len(got) != 1 || got[0].prefix.String() != "2001:db8::/32" ||
		got[0].gateway.String() != "fe80::1" || got[0].metric != 1024 || got[0].device != "br-lan" {
		t.Errorf("got %+v", got)
	}
}

// D-74: "working" is what the kernel holds, never netifd's status.
func TestARouteIsActiveOnlyWhenTheKernelHasExactlyIt(t *testing.T) {
	m, _ := routeManager(t, routesShow, procRoutes)
	st, err := m.StaticRoutes()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Routes) != 4 {
		t.Fatalf("routes = %+v", st.Routes)
	}
	r0, r1, r2, r3 := st.Routes[0], st.Routes[1], st.Routes[2], st.Routes[3]
	if !r0.Active || r0.Target != "10.20.0.0/16" || r0.Gateway != "192.168.1.2" || r0.Name != "Office" {
		t.Errorf("route 0 = %+v", r0)
	}
	if !r1.Active || r1.Target != "172.16.5.0/24" || r1.Metric != 10 {
		t.Errorf("route 1 (target + netmask) = %+v", r1)
	}
	if r2.Active || !slices.Equal(r2.Unsupported, []string{core.RouteTable}) {
		t.Errorf("route 2 (own table) = %+v", r2)
	}
	if r3.Active || r3.Enabled {
		t.Errorf("route 3 (switched off) = %+v", r3)
	}
	if len(st.Interfaces) != 2 || st.Interfaces[0].Name != "lan" {
		t.Errorf("interfaces = %+v, want lan and wan without loopback", st.Interfaces)
	}

	// E5, measured: something else replaced the route with its own gateway.
	// netifd still shows its own; the kernel does not have this route.
	replaced := strings.Replace(procRoutes, "0000140A\t0201A8C0", "0000140A\t0901A8C0", 1)
	m, _ = routeManager(t, routesShow, replaced)
	st, _ = m.StaticRoutes()
	if st.Routes[0].Active {
		t.Error("a route whose gateway the kernel does not have reads as active")
	}
	// The same network and gateway through another device is not this route.
	elsewhere := strings.Replace(procRoutes, "br-lan\t0000140A", "wan\t0000140A", 1)
	m, _ = routeManager(t, routesShow, elsewhere)
	st, _ = m.StaticRoutes()
	if st.Routes[0].Active {
		t.Error("a route the kernel holds on another device reads as active")
	}
	// Another metric is another route.
	remetric := strings.Replace(procRoutes, "0003\t0\t0\t10\t", "0003\t0\t0\t20\t", 1)
	m, _ = routeManager(t, routesShow, remetric)
	st, _ = m.StaticRoutes()
	if st.Routes[1].Active {
		t.Error("a route with another metric in the kernel reads as active")
	}
}

// netifd gives a route with no metric of its own the interface's metric.
func TestARouteWithoutAMetricMatchesTheInterfaceMetric(t *testing.T) {
	show := strings.Replace(routesShow, "network.lan.proto='static'\n",
		"network.lan.proto='static'\nnetwork.lan.metric='5'\n", 1)
	kernel := strings.Replace(procRoutes, "0000140A\t0201A8C0\t0003\t0\t0\t0\t", "0000140A\t0201A8C0\t0003\t0\t0\t5\t", 1)
	m, _ := routeManager(t, show, kernel)
	st, _ := m.StaticRoutes()
	if !st.Routes[0].Active {
		t.Error("a route that took the interface's metric reads as not active")
	}
}

func TestANewRouteIsOneRowWithTheNetworkTheKernelWillGet(t *testing.T) {
	m, r := routeManager(t, routesShow, procRoutes)
	cs, err := m.StageStaticRoute(core.StaticRouteConfig{
		Enabled: true, Target: "10.8.0.77/24", Gateway: "192.168.1.2", Interface: "lan", Name: "VPN box",
	})
	if err != nil {
		t.Fatal(err)
	}
	staged := strings.Join(r.uci.sets(), "|")
	for _, want := range []string{
		"network.cfg05fe63.interface=lan", "network.cfg05fe63.target=10.8.0.0/24",
		"network.cfg05fe63.gateway=192.168.1.2", "network.cfg05fe63.name=VPN box",
	} {
		if !strings.Contains(staged, want) {
			t.Errorf("staged %q, want %q", staged, want)
		}
	}
	if strings.Contains(staged, "disabled") || strings.Contains(staged, "metric") {
		t.Errorf("staged %q: an on route with metric 0 needs neither option", staged)
	}
	if len(cs) != 1 || cs[0].LabelKey != "network.route.section" ||
		cs[0].To != "10.8.0.0/24 via 192.168.1.2 (lan)" || !cs[0].Dangerous {
		t.Errorf("rows = %+v", cs)
	}
	if r.called("uci commit") || r.called("/sbin/reload_config") {
		t.Fatal("a route was applied instead of staged")
	}
}

func TestTheConnectionIsPickedByTheGateway(t *testing.T) {
	m, r := routeManager(t, routesShow, procRoutes)
	if _, err := m.StageStaticRoute(core.StaticRouteConfig{
		Enabled: true, Target: "10.9.0.0/24", Gateway: "198.51.100.7",
	}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(r.uci.sets(), "network.cfg05fe63.interface=wan") {
		t.Errorf("sets = %v, want the connection whose network holds the gateway", r.uci.sets())
	}
}

// D-75: netifd accepts such a route and the kernel never gets it.
func TestAGatewayOffTheConnectionsNetworkIsRefused(t *testing.T) {
	cases := map[string]core.StaticRouteConfig{
		"off the network":     {Enabled: true, Target: "10.9.0.0/24", Gateway: "10.99.99.1", Interface: "lan"},
		"the router itself":   {Enabled: true, Target: "10.9.0.0/24", Gateway: "192.168.1.1", Interface: "lan"},
		"on no connection":    {Enabled: true, Target: "10.9.0.0/24", Gateway: "10.99.99.1"},
		"on the other one":    {Enabled: true, Target: "10.9.0.0/24", Gateway: "198.51.100.7", Interface: "lan"},
		"not an IPv4 address": {Enabled: true, Target: "10.9.0.0/24", Gateway: "fe80::1", Interface: "lan"},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			m, r := routeManager(t, routesShow, procRoutes)
			_, err := m.StageStaticRoute(cfg)
			assertRefusedField(t, err, "gateway")
			if r.called("uci add") || len(r.uci.sets()) != 0 {
				t.Errorf("the device was touched before refusing: %v", r.uci.calls)
			}
		})
	}
}

func TestRouteFieldsAreRefusedWhereTheyAre(t *testing.T) {
	cases := map[string]struct {
		cfg   core.StaticRouteConfig
		field string
	}{
		"no network":        {core.StaticRouteConfig{Target: "", Gateway: "192.168.1.2"}, "target"},
		"IPv6 network":      {core.StaticRouteConfig{Target: "2001:db8::/32", Interface: "lan"}, "target"},
		"no way to go":      {core.StaticRouteConfig{Target: "10.9.0.0/24"}, "interface"},
		"unknown link":      {core.StaticRouteConfig{Target: "10.9.0.0/24", Interface: "vpn0"}, "interface"},
		"metric too large":  {core.StaticRouteConfig{Target: "10.9.0.0/24", Interface: "lan", Metric: 70000}, "metric"},
		"quote in the name": {core.StaticRouteConfig{Target: "10.9.0.0/24", Interface: "lan", Name: "it's"}, "name"},
		"unknown route":     {core.StaticRouteConfig{ID: "@route[9]", Target: "10.9.0.0/24", Interface: "lan"}, "id"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			m, _ := routeManager(t, routesShow, procRoutes)
			_, err := m.StageStaticRoute(c.cfg)
			assertRefusedField(t, err, c.field)
		})
	}
}

// D-76, measured: netifd takes a matching foreign route over and removes it
// together with its own; with another gateway it silently replaces it.
func TestARouteTheKernelHasFromSomebodyElseIsRefusedByName(t *testing.T) {
	// The tunnel engine's route, as it writes it today: main table, its device.
	tunnel := procRoutes + "awg0\t007100CB\t00000000\t0001\t0\t0\t0\t00FFFFFF\t0\t0\t0\n"
	m, r := routeManager(t, routesShow, tunnel)
	_, err := m.StageStaticRoute(core.StaticRouteConfig{
		Enabled: true, Target: "203.0.113.0/24", Gateway: "192.168.1.2", Interface: "lan",
	})
	assertRefusedField(t, err, "target")
	if !strings.Contains(err.Error(), "awg0") {
		t.Errorf("err = %v, want it to name the owner", err)
	}
	if r.called("uci add") {
		t.Error("the device was touched before refusing")
	}

	// Another metric lives next to it (E6) and is fine.
	m, _ = routeManager(t, routesShow, tunnel)
	if _, err := m.StageStaticRoute(core.StaticRouteConfig{
		Enabled: true, Target: "203.0.113.0/24", Gateway: "192.168.1.2", Interface: "lan", Metric: 10,
	}); err != nil {
		t.Errorf("another metric was refused: %v", err)
	}

	// A switched-off route cannot take anything over.
	m, _ = routeManager(t, routesShow, tunnel)
	if _, err := m.StageStaticRoute(core.StaticRouteConfig{
		Enabled: false, Target: "203.0.113.0/24", Gateway: "192.168.1.2", Interface: "lan",
	}); err != nil {
		t.Errorf("a switched-off route was refused: %v", err)
	}
}

func TestAConnectedNetworkOrASecondRouteToTheSameNetworkIsRefused(t *testing.T) {
	m, _ := routeManager(t, routesShow, procRoutes)
	_, err := m.StageStaticRoute(core.StaticRouteConfig{
		Enabled: true, Target: "192.168.1.0/24", Interface: "lan",
	})
	assertRefusedField(t, err, "target")
	if !strings.Contains(err.Error(), "lan") {
		t.Errorf("err = %v, want it to name the connection", err)
	}

	m, _ = routeManager(t, routesShow, procRoutes)
	_, err = m.StageStaticRoute(core.StaticRouteConfig{
		Enabled: true, Target: "10.20.0.0/16", Gateway: "192.168.1.9", Interface: "lan",
	})
	assertRefusedField(t, err, "target")
	if !strings.Contains(err.Error(), "Office") {
		t.Errorf("err = %v, want it to name the route already there", err)
	}
}

// The route's own kernel entry is not a clash with itself.
func TestEditingARouteStagesOnlyWhatChangedAndSaysWhichRoute(t *testing.T) {
	m, r := routeManager(t, routesShow, procRoutes)
	cs, err := m.StageStaticRoute(core.StaticRouteConfig{
		ID: "@route[0]", Enabled: true, Target: "10.20.0.0/16", Gateway: "192.168.1.9",
		Interface: "lan", Name: "Office",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.uci.sets(); !slices.Equal(got, []string{"network.@route[0].gateway=192.168.1.9"}) {
		t.Errorf("sets = %v, want only the gateway", got)
	}
	if len(cs) != 1 || cs[0].LabelKey != "network.route.gateway" || cs[0].From != "192.168.1.2" ||
		cs[0].Subject != "Office" {
		t.Errorf("rows = %+v", cs)
	}
}

// A route written as target + netmask keeps that form, or an edit of its
// gateway would also show two rows about its target.
func TestARouteKeepsTheFormItWasWrittenIn(t *testing.T) {
	m, r := routeManager(t, routesShow, procRoutes)
	if _, err := m.StageStaticRoute(core.StaticRouteConfig{
		ID: "@route[1]", Enabled: true, Target: "172.16.5.0/24", Gateway: "192.168.1.8",
		Interface: "lan", Metric: 10,
	}); err != nil {
		t.Fatal(err)
	}
	if got := r.uci.sets(); !slices.Equal(got, []string{"network.@route[1].gateway=192.168.1.8"}) {
		t.Errorf("sets = %v", got)
	}
}

func TestARouteWithHiddenSettingsIsOnlySwitchedOnAndOff(t *testing.T) {
	m, _ := routeManager(t, routesShow, procRoutes)
	_, err := m.StageStaticRoute(core.StaticRouteConfig{
		ID: "@route[2]", Enabled: true, Target: "172.16.9.0/24", Gateway: "192.168.1.6", Interface: "lan",
	})
	assertRefusedField(t, err, "id")

	m, r := routeManager(t, routesShow, procRoutes)
	cs, err := m.StageStaticRoute(core.StaticRouteConfig{
		ID: "@route[2]", Enabled: false, Target: "172.16.9.0/24", Gateway: "192.168.1.4", Interface: "lan",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.uci.sets(); !slices.Equal(got, []string{"network.@route[2].disabled=1"}) {
		t.Errorf("sets = %v", got)
	}
	if len(cs) != 1 || cs[0].LabelKey != "network.route.disabled" {
		t.Errorf("rows = %+v", cs)
	}
}

func TestSwitchingARouteBackOnRemovesTheOption(t *testing.T) {
	m, r := routeManager(t, routesShow, procRoutes)
	if _, err := m.StageStaticRoute(core.StaticRouteConfig{
		ID: "@route[3]", Enabled: true, Target: "172.16.7.0/24", Gateway: "192.168.1.5", Interface: "lan",
	}); err != nil {
		t.Fatal(err)
	}
	if !r.called("uci delete network.@route[3].disabled") || len(r.uci.sets()) != 0 {
		t.Errorf("calls = %v", r.uci.calls)
	}
}

func TestRemovingARouteIsOneRowInWords(t *testing.T) {
	m, r := routeManager(t, routesShow, procRoutes)
	cs, err := m.RemoveStaticRoute("@route[0]")
	if err != nil {
		t.Fatal(err)
	}
	if !r.called("uci delete network.@route[0]") {
		t.Errorf("calls = %v", r.uci.calls)
	}
	if len(cs) != 1 || cs[0].From != "10.20.0.0/16 via 192.168.1.2 (lan)" || cs[0].To != "" ||
		cs[0].LabelKey != "network.route.section" || !cs[0].Dangerous {
		t.Errorf("rows = %+v", cs)
	}
	if _, err := m.RemoveStaticRoute("@route[7]"); err == nil {
		t.Error("removing a route that is not there succeeded")
	}
	if _, err := m.RemoveStaticRoute("wan; reboot"); err == nil {
		t.Error("a malformed id was accepted")
	}
}

// After a reload the draft is read back off the device: a new route is one
// row in the same words staging used, not the section's type.
func TestAStagedRouteReadsBackAsOneRow(t *testing.T) {
	r := &recordingRunner{
		values: map[string]string{
			"network.cfg0a1b2c":           "route",
			"network.cfg0a1b2c.interface": "lan",
			"network.cfg0a1b2c.target":    "10.8.0.0/24",
			"network.cfg0a1b2c.gateway":   "192.168.1.2",
		},
		committed: map[string]string{},
		stagedLines: []string{
			"network.cfg0a1b2c='route'",
			"network.cfg0a1b2c.interface='lan'",
			"network.cfg0a1b2c.target='10.8.0.0/24'",
			"network.cfg0a1b2c.gateway='192.168.1.2'",
		},
		anonymous: map[string]string{"cfg0a1b2c": "@route[0]"},
	}
	m := networkManager{run: r.run, configDir: t.TempDir()}
	cs, err := m.StagedChanges()
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 || cs[0].LabelKey != "network.route.section" ||
		cs[0].To != "10.8.0.0/24 via 192.168.1.2 (lan)" {
		t.Errorf("rows = %+v", cs)
	}
}

// A removal is one line, `-network.cfg0a1b2c`, and nothing else: the route's
// fields have to be asked for by the internal name, or the row says nothing
// about which route is going away.
func TestARemovedRouteReadsBackInWords(t *testing.T) {
	r := &recordingRunner{
		values: map[string]string{},
		committed: map[string]string{
			"network.cfg0a1b2c":           "route",
			"network.cfg0a1b2c.interface": "lan",
			"network.cfg0a1b2c.target":    "10.8.0.0",
			"network.cfg0a1b2c.netmask":   "255.255.255.0",
			"network.cfg0a1b2c.gateway":   "192.168.1.2",
		},
		stagedLines: []string{"-network.cfg0a1b2c"},
		anonymous:   map[string]string{"cfg0a1b2c": "@route[0]"},
	}
	m := networkManager{run: r.run, configDir: t.TempDir()}
	cs, err := m.StagedChanges()
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 || cs[0].LabelKey != "network.route.section" ||
		cs[0].From != "10.8.0.0/24 via 192.168.1.2 (lan)" || cs[0].To != "" {
		t.Errorf("rows = %+v", cs)
	}
}

func TestRoutesNeedARunner(t *testing.T) {
	var m networkManager
	if _, err := m.StaticRoutes(); !errors.Is(err, core.ErrNotImplemented) {
		t.Errorf("read: %v", err)
	}
	if _, err := m.StageStaticRoute(core.StaticRouteConfig{}); !errors.Is(err, core.ErrNotImplemented) {
		t.Errorf("stage: %v", err)
	}
	if _, err := m.RemoveStaticRoute("@route[0]"); !errors.Is(err, core.ErrNotImplemented) {
		t.Errorf("remove: %v", err)
	}
}
