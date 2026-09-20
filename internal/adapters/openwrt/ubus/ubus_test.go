package ubus

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The fixtures are verbatim `ubus call …` output from two live stands. The
// addresses of the network they were captured on are rewritten to the
// documentation range (RFC 5737); 192.168.1.1 is left alone, because that is
// OpenWrt's factory LAN address and not anybody's. Both branches are replayed in
// every test that parses a reply, because the point of having two is that they
// disagree: 25.12 carries release fields 23.05 does not. The zeros in the
// 23.05 load average are not a branch difference — that stand was simply idle
// when the fixture was captured (re-measured under load 20.09.2026: both
// branches report the same value as /proc/loadavg).
var branches = []string{"23.05", "25.12"}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return b
}

// replay returns a runner that answers with a fixture and records the command
// it was asked to run.
func replay(t *testing.T, name string, got *[]string) Runner {
	t.Helper()
	return func(_ context.Context, cmd string, args ...string) ([]byte, error) {
		if got != nil {
			*got = append([]string{cmd}, args...)
		}
		return fixture(t, name), nil
	}
}

func TestBoardIsParsedOnBothBranches(t *testing.T) {
	// The whole struct, not a few fields: a wrong json tag on any of them
	// would otherwise reach a dashboard as an empty string.
	want := map[string]Board{
		"23.05": {
			Kernel:     "5.15.167",
			Hostname:   "OpenWrt",
			System:     "Intel(R) N100",
			Model:      "QEMU Ubuntu 24.04 PC v2 (i440FX + PIIX, arch_caps fix, 1996)",
			BoardName:  "qemu-ubuntu-24-04-pc-v2-i440fx-piix-arch_caps-fix-1996",
			RootfsType: "ext4",
			Release: Release{
				Distribution: "OpenWrt",
				Version:      "23.05.5",
				Revision:     "r24106-10cc5fcd00",
				Target:       "x86/64",
				Description:  "OpenWrt 23.05.5 r24106-10cc5fcd00",
			},
		},
		"25.12": {
			Kernel:     "6.12.94",
			Hostname:   "OpenWrt",
			System:     "ARMv8 Processor rev 4",
			Model:      "Cudy WR3000S v1",
			BoardName:  "cudy,wr3000s-v1",
			RootfsType: "squashfs",
			Release: Release{
				Distribution: "OpenWrt",
				Version:      "25.12.5",
				Revision:     "r33051-f5dae5ece4",
				Target:       "mediatek/filogic",
				Description:  "OpenWrt 25.12.5 r33051-f5dae5ece4",
				FirmwareURL:  "https://downloads.openwrt.org/",
				BuildDate:    "1782737960",
			},
		},
	}
	for _, br := range branches {
		t.Run(br, func(t *testing.T) {
			var cmd []string
			c := NewWithRunner(replay(t, "board-"+br+".json", &cmd))

			b, err := c.Board(context.Background())
			if err != nil {
				t.Fatalf("board: %v", err)
			}
			if b != want[br] {
				t.Errorf("board =\n  %+v\nwant\n  %+v", b, want[br])
			}
			if strings.Join(cmd, " ") != "/bin/ubus call system board" {
				t.Errorf("ran %q", strings.Join(cmd, " "))
			}
		})
	}
}

// 25.12 added release.firmware_url and release.builddate. Extra fields on a
// newer branch must not break the older one, and must not be silently dropped
// on the newer one either.
func TestNewerBranchFieldsAreKeptAndOptional(t *testing.T) {
	newer, err := NewWithRunner(replay(t, "board-25.12.json", nil)).Board(context.Background())
	if err != nil {
		t.Fatalf("board 25.12: %v", err)
	}
	if newer.Release.FirmwareURL == "" || newer.Release.BuildDate == "" {
		t.Errorf("25.12 fields dropped: url=%q builddate=%q",
			newer.Release.FirmwareURL, newer.Release.BuildDate)
	}

	older, err := NewWithRunner(replay(t, "board-23.05.json", nil)).Board(context.Background())
	if err != nil {
		t.Fatalf("board 23.05: %v", err)
	}
	if older.Release.FirmwareURL != "" || older.Release.BuildDate != "" {
		t.Errorf("23.05 invented fields it does not have: %+v", older.Release)
	}
}

func TestSystemInfoParsesMemoryAndUptime(t *testing.T) {
	for _, br := range branches {
		t.Run(br, func(t *testing.T) {
			c := NewWithRunner(replay(t, "info-"+br+".json", nil))
			i, err := c.SystemInfo(context.Background())
			if err != nil {
				t.Fatalf("system info: %v", err)
			}
			if i.Uptime <= 0 {
				t.Errorf("uptime = %d", i.Uptime)
			}
			if i.Memory.Total <= 0 || i.Memory.Available <= 0 {
				t.Errorf("memory = %+v", i.Memory)
			}
			if i.Memory.Available > i.Memory.Total {
				t.Errorf("available %d exceeds total %d", i.Memory.Available, i.Memory.Total)
			}
			if i.Root.Total <= 0 {
				t.Errorf("root filesystem = %+v", i.Root)
			}
		})
	}
}

// The load triple is fixed point scaled by 65536. Reporting it raw would put
// "load 1216" on a dashboard of a router that is nearly idle.
func TestLoadAverageIsScaled(t *testing.T) {
	// A known input first: the fixture is a live capture and can legitimately
	// be all zeros on an idle router, which would make an assertion built on
	// it prove nothing.
	if got := (Info{Load: [3]int64{65536, 32768, 131072}}).LoadAverage(); got != [3]float64{1, 0.5, 2} {
		t.Errorf("load average of 65536/32768/131072 = %v, want [1 0.5 2]", got)
	}

	// A capture taken while the router was actually doing something. Live
	// fixtures are legitimately all zeros on an idle device, which would make
	// a scaling assertion built on them prove nothing.
	busy := `{"localtime":1789898235,"uptime":10144,"load":[1216,1280,0],` +
		`"memory":{"total":245317632,"free":72040448,"available":69193728},"swap":{"total":0,"free":0}}`
	i, err := NewWithRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte(busy), nil
	}).SystemInfo(context.Background())
	if err != nil {
		t.Fatalf("system info: %v", err)
	}
	if i.Load != [3]int64{1216, 1280, 0} {
		t.Fatalf("raw load = %v, want the captured [1216 1280 0]", i.Load)
	}
	if got := i.LoadAverage()[0]; got < 0.018 || got > 0.019 {
		t.Errorf("load average = %f, want ~0.0186 (1216/65536)", got)
	}

	for _, br := range branches {
		t.Run(br, func(t *testing.T) {
			i, err := NewWithRunner(replay(t, "info-"+br+".json", nil)).SystemInfo(context.Background())
			if err != nil {
				t.Fatalf("system info: %v", err)
			}
			// Whatever the device was doing, a load average is a small number.
			// Anything in the thousands means the raw fixed-point value reached
			// the dashboard.
			if got := i.LoadAverage()[0]; got < 0 || got > 100 {
				t.Errorf("load average = %f, raw = %d - that is not a load average", got, i.Load[0])
			}
		})
	}
}

func TestInterfacesAreParsed(t *testing.T) {
	// Field by field on the interface that matters - the uplink - because a
	// wrong tag on l3_device or ipv4-address is exactly the kind of mistake
	// that only shows up on a router, at the worst moment.
	want := map[string]Interface{
		"23.05": {
			Name: "lanwan", Up: true, Available: true, Autostart: true,
			Proto: "dhcp", Device: "eth1", L3Device: "eth1",
			IPv4Address: []Address{{Address: "198.51.100.116", Mask: 24}},
		},
		"25.12": {
			Name: "wan", Up: true, Available: true, Autostart: true,
			Proto: "dhcp", Device: "wan", L3Device: "wan",
			IPv4Address: []Address{{Address: "198.51.100.242", Mask: 24}},
		},
	}
	for _, br := range branches {
		t.Run(br, func(t *testing.T) {
			var cmd []string
			c := NewWithRunner(replay(t, "ifdump-"+br+".json", &cmd))

			ifaces, err := c.Interfaces(context.Background())
			if err != nil {
				t.Fatalf("interfaces: %v", err)
			}
			if strings.Join(cmd, " ") != "/bin/ubus call network.interface dump" {
				t.Errorf("ran %q", strings.Join(cmd, " "))
			}

			w := want[br]
			var found *Interface
			for i := range ifaces {
				if ifaces[i].Name == w.Name {
					found = &ifaces[i]
				}
			}
			if found == nil {
				t.Fatalf("interface %q missing from the dump of %d entries", w.Name, len(ifaces))
			}
			if found.Up != w.Up || found.Available != w.Available || found.Autostart != w.Autostart {
				t.Errorf("flags: up=%v available=%v autostart=%v, want %v/%v/%v",
					found.Up, found.Available, found.Autostart, w.Up, w.Available, w.Autostart)
			}
			if found.Proto != w.Proto || found.Device != w.Device || found.L3Device != w.L3Device {
				t.Errorf("proto=%q device=%q l3=%q, want %q/%q/%q",
					found.Proto, found.Device, found.L3Device, w.Proto, w.Device, w.L3Device)
			}
			if len(found.IPv4Address) != 1 || found.IPv4Address[0] != w.IPv4Address[0] {
				t.Errorf("ipv4 = %+v, want %+v", found.IPv4Address, w.IPv4Address)
			}
			if found.Uptime <= 0 {
				t.Errorf("uptime = %d on an interface that is up", found.Uptime)
			}

			// The loopback is the one entry every device has, and its
			// device/l3_device pair is the simplest sanity check of the dump.
			var loopback bool
			for _, i := range ifaces {
				if i.Name == "loopback" {
					loopback, _ = true, i
					if i.Device != "lo" || i.L3Device != "lo" || i.Proto != "static" {
						t.Errorf("loopback = %+v", i)
					}
				}
			}
			if !loopback {
				t.Error("no loopback interface in the dump")
			}
		})
	}
}

// The uplink is the interface with a default route, and finding it is the
// whole reason NetworkManager will call this. Both fixtures are checked,
// because they disagree: the VM stand has two networks with a gateway, the
// router has one.
func TestDefaultRouteFindsTheUplinks(t *testing.T) {
	want := map[string][]string{
		"23.05": {"lan=198.51.100.1", "lanwan=198.51.100.1"},
		"25.12": {"wan=198.51.100.1"},
	}
	for _, br := range branches {
		t.Run(br, func(t *testing.T) {
			ifaces, err := NewWithRunner(replay(t, "ifdump-"+br+".json", nil)).Interfaces(context.Background())
			if err != nil {
				t.Fatalf("interfaces: %v", err)
			}
			var got []string
			for _, i := range ifaces {
				if gw, ok := i.DefaultRoute(); ok {
					got = append(got, i.Name+"="+gw)
				}
			}
			sort.Strings(got)
			if strings.Join(got, ",") != strings.Join(want[br], ",") {
				t.Fatalf("default routes = %v, want %v", got, want[br])
			}
		})
	}
}

// IPv6 has its own default route and its own nexthop, so the two families are
// asked for separately: handing a caller who wants IPv4 an fe80:: nexthop
// would point traffic at a gateway that does not serve it.
func TestDefaultRoutesAreSeparatePerFamily(t *testing.T) {
	both := Interface{Route: []Route{
		{Target: "0.0.0.0", Mask: 0, Nexthop: "198.51.100.1"},
		{Target: "::", Mask: 0, Nexthop: "fe80::1"},
	}}
	if gw, ok := both.DefaultRoute(); !ok || gw != "198.51.100.1" {
		t.Errorf("IPv4 gateway = %q (%v), want 198.51.100.1", gw, ok)
	}
	if gw, ok := both.DefaultRoute6(); !ok || gw != "fe80::1" {
		t.Errorf("IPv6 gateway = %q (%v), want fe80::1", gw, ok)
	}

	// And what the router actually looked like: wan6 carries ULA routes from
	// router advertisements but no ::/0 at all. Neither method may invent one
	// out of a /48 or a /64.
	ifaces, err := NewWithRunner(replay(t, "ifdump-25.12.json", nil)).Interfaces(context.Background())
	if err != nil {
		t.Fatalf("interfaces: %v", err)
	}
	var wan6 *Interface
	for i := range ifaces {
		if ifaces[i].Name == "wan6" {
			wan6 = &ifaces[i]
		}
	}
	if wan6 == nil {
		t.Fatal("no wan6 in the fixture")
	}
	if len(wan6.Route) == 0 {
		t.Fatal("fixture changed: wan6 has no routes, so this proves nothing")
	}
	if gw, ok := wan6.DefaultRoute6(); ok {
		t.Errorf("wan6 reported %q as an IPv6 default route, but the capture has only prefix routes", gw)
	}
	if gw, ok := wan6.DefaultRoute(); ok {
		t.Errorf("wan6 reported an IPv4 default route %q", gw)
	}
}

// A host route to 0.0.0.0 is not a default route. Ignoring the mask would make
// one look like an uplink.
func TestHostRouteToZeroIsNotADefaultRoute(t *testing.T) {
	i := Interface{Route: []Route{{Target: "0.0.0.0", Mask: 32, Nexthop: "198.51.100.1"}}}
	if gw, ok := i.DefaultRoute(); ok {
		t.Fatalf("0.0.0.0/32 was read as a default route via %q", gw)
	}
}

// An entry with no gateway (an on-link default) must not be reported as one
// either: callers use the value to reach something.
func TestDefaultRouteWithoutNexthopIsNotReported(t *testing.T) {
	i := Interface{Route: []Route{{Target: "0.0.0.0", Mask: 0}}}
	if _, ok := i.DefaultRoute(); ok {
		t.Fatal("a default route with no nexthop was reported as usable")
	}
}

// Every call has to be bounded: ubus can block forever on a wedged netifd, and
// a dashboard poll that never returns is how a panel stops answering.
func TestEveryCallIsBounded(t *testing.T) {
	var hadDeadline bool
	c := NewWithRunner(func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		_, hadDeadline = ctx.Deadline()
		return []byte("{}"), nil
	})
	var out map[string]any
	if err := c.Call(context.Background(), "system", "board", &out); err != nil {
		t.Fatalf("call: %v", err)
	}
	if !hadDeadline {
		t.Fatal("the command ran with no deadline on its context")
	}
}

func TestCallFailureIsReportedNotParsed(t *testing.T) {
	c := NewWithRunner(func(context.Context, string, ...string) ([]byte, error) {
		// What ubus does for an unknown object: status 4, message on stderr.
		return nil, errors.New("ubus call nosuch method: exit status 4: Command failed: Not found")
	})
	var out map[string]any
	err := c.Call(context.Background(), "nosuch", "method", &out)
	if err == nil {
		t.Fatal("a failed ubus call was reported as success")
	}
	// The device's own words have to survive to the log: "exit status 4" alone
	// is not something anyone can act on.
	if !strings.Contains(err.Error(), "Command failed: Not found") {
		t.Errorf("error = %v, want it to carry what ubus said", err)
	}
	if out != nil {
		t.Errorf("output was written despite the failure: %v", out)
	}
}

// A method that prints nothing must not surface as a JSON syntax error: the
// caller has to be able to tell "no such data" from "broken data".
func TestEmptyReplyIsItsOwnError(t *testing.T) {
	c := NewWithRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("\n"), nil
	})
	var out map[string]any
	err := c.Call(context.Background(), "system", "board", &out)
	if err == nil || !strings.Contains(err.Error(), "empty reply") {
		t.Fatalf("error = %v, want one that says the reply was empty", err)
	}
}

func TestGarbageReplyIsADecodeError(t *testing.T) {
	c := NewWithRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("Command failed: Not found"), nil
	})
	var out map[string]any
	if err := c.Call(context.Background(), "system", "board", &out); err == nil {
		t.Fatal("non-JSON output was accepted")
	}
}

// The seam is an allow-list, not a convenience: it must refuse anything that
// is not ubus even when called directly.
func TestRunCommandRefusesForeignPrograms(t *testing.T) {
	if _, err := runCommand(context.Background(), "sh", "-c", "echo hi"); err == nil {
		t.Fatal("runCommand executed a program outside the allow-list")
	}
}

// The message ubus prints is the only diagnosis a router offers, so it must
// reach the error - from stderr normally, from stdout when ubus chose that,
// and with an explicit note when there was none at all.
func TestCallErrorCarriesWhatTheDeviceSaid(t *testing.T) {
	base := errors.New("exit status 4")
	for _, tc := range []struct{ name, stdout, stderr, want string }{
		{"stderr", "", "Command failed: Not found", "Command failed: Not found"},
		{"stdout only", "Command failed: Permission denied", "", "Command failed: Permission denied"},
		{"silence", "", "", "no message from ubus"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := callError([]string{"call", "system", "board"}, base, tc.stdout, tc.stderr)
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to contain %q", err, tc.want)
			}
			if !errors.Is(err, base) {
				t.Error("the underlying exec error was not wrapped")
			}
			if !strings.Contains(err.Error(), "call system board") {
				t.Errorf("error = %q, want it to name the failed call", err)
			}
		})
	}
}
