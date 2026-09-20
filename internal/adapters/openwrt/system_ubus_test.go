package openwrt

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/veilbridge-os/veilbridge/internal/adapters/openwrt/ubus"
)

// M1.4: the dashboard's system snapshot comes from the device's own account of
// itself. /proc cannot name a board, a firmware release, or the overlay that
// fills up first on a flash router — ubus can, and these tests replay both
// branches because the two disagree about the load average.

func TestSystemInfoIsEnrichedFromUbus(t *testing.T) {
	want := map[string]struct {
		model, firmware, kernel string
		uptime                  int64
		memTotal                int64
	}{
		"23.05": {
			model:    "QEMU Ubuntu 24.04 PC v2 (i440FX + PIIX, arch_caps fix, 1996)",
			firmware: "OpenWrt 23.05.5 r24106-10cc5fcd00",
			kernel:   "5.15.167",
		},
		"25.12": {
			model:    "Cudy WR3000S v1",
			firmware: "OpenWrt 25.12.5 r33051-f5dae5ece4",
			kernel:   "6.12.94",
		},
	}
	for _, branch := range ubusBranches {
		t.Run(branch, func(t *testing.T) {
			v, _ := newTestVPN(t, &fakeEngine{})
			sys := newSystemManager(v, busReplay(t, branch, nil))
			// Keep the probe off the network: Info() would otherwise try to
			// resolve a public IP from a test run.
			sys.egress = func(func(*http.Request) (*http.Response, error)) string { return "" }

			info, err := sys.Info()
			if err != nil {
				t.Fatalf("info: %v", err)
			}
			w := want[branch]
			if info.Model != w.model {
				t.Errorf("model = %q, want %q", info.Model, w.model)
			}
			if info.Firmware != w.firmware {
				t.Errorf("firmware = %q, want %q", info.Firmware, w.firmware)
			}
			if info.Kernel != w.kernel {
				t.Errorf("kernel = %q, want %q", info.Kernel, w.kernel)
			}
			if info.UptimeSec <= 0 {
				t.Errorf("uptime not taken from ubus: %d", info.UptimeSec)
			}
			if info.MemTotal <= 0 || info.MemUsed <= 0 || info.MemUsed >= info.MemTotal {
				t.Errorf("memory implausible: used=%d total=%d", info.MemUsed, info.MemTotal)
			}
			// The overlay is the number that matters on a flash router: it is
			// what runs out, and /proc/meminfo says nothing about it.
			if info.StorageTotal <= 0 || info.StorageUsed <= 0 {
				t.Errorf("storage not reported: used=%d total=%d", info.StorageUsed, info.StorageTotal)
			}
			if info.StorageUsed > info.StorageTotal {
				t.Errorf("storage used %d exceeds total %d", info.StorageUsed, info.StorageTotal)
			}
		})
	}
}

// Memory must be counted as total-available, not total-free: "free" treats the
// page cache as used and would show a healthy router permanently at ~95%.
func TestMemoryExcludesCache(t *testing.T) {
	v, _ := newTestVPN(t, &fakeEngine{})
	bus := ubus.NewWithRunner(func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[1] == "system" && args[2] == "info" {
			return []byte(`{"uptime":100,"load":[0,0,0],
				"memory":{"total":1000,"free":100,"available":600,"cached":500},
				"root":{"total":10,"free":4,"used":6,"avail":4}}`), nil
		}
		return []byte(`{}`), nil
	})
	sys := newSystemManager(v, bus)
	sys.egress = func(func(*http.Request) (*http.Response, error)) string { return "" }

	info, _ := sys.Info()
	if info.MemUsed != 400 {
		t.Errorf("memUsed = %d, want 400 (total-available, not total-free=900)", info.MemUsed)
	}
}

// ubus reports filesystem blocks in kilobytes while memory is in bytes. Mixing
// the two would understate a full overlay by a factor of 1024.
func TestStorageIsConvertedFromKilobytes(t *testing.T) {
	v, _ := newTestVPN(t, &fakeEngine{})
	bus := ubus.NewWithRunner(func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[1] == "system" && args[2] == "info" {
			return []byte(`{"uptime":1,"load":[0,0,0],"memory":{"total":0},
				"root":{"total":45756,"free":2424,"used":43332,"avail":2424}}`), nil
		}
		return []byte(`{}`), nil
	})
	sys := newSystemManager(v, bus)
	sys.egress = func(func(*http.Request) (*http.Response, error)) string { return "" }

	info, _ := sys.Info()
	if info.StorageTotal != 45756*1024 || info.StorageUsed != 43332*1024 {
		t.Errorf("storage = %d/%d bytes, want %d/%d",
			info.StorageUsed, info.StorageTotal, 43332*1024, 45756*1024)
	}
}

// A dead or missing ubus must cost detail, never the dashboard: the /proc
// reading has to survive, and Info() must not return an error.
func TestSystemInfoSurvivesADeadUbus(t *testing.T) {
	v, _ := newTestVPN(t, &fakeEngine{})
	boom := ubus.NewWithRunner(func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("Command failed: Not found")
	})
	sys := newSystemManager(v, boom)
	sys.egress = func(func(*http.Request) (*http.Response, error)) string { return "" }

	info, err := sys.Info()
	if err != nil {
		t.Fatalf("a failed enrichment must not fail the dashboard: %v", err)
	}
	if info.Platform != "openwrt" {
		t.Errorf("platform lost: %q", info.Platform)
	}
	// /proc answers on any Linux CI runner; on other hosts it is legitimately
	// zero, so assert only that nothing was invented.
	if info.Model != "" || info.Firmware != "" || info.Kernel != "" {
		t.Errorf("fields invented from a failed bus: %+v", info)
	}
}

// The board never changes while the daemon runs, and the dashboard polls. One
// fork per poll on a 256 MB router is a cost with nothing to buy.
func TestBoardIsReadOncePerProcess(t *testing.T) {
	v, _ := newTestVPN(t, &fakeEngine{})
	calls := 0
	sys := newSystemManager(v, busReplay(t, "25.12", &calls))
	sys.egress = func(func(*http.Request) (*http.Response, error)) string { return "" }

	for i := 0; i < 3; i++ {
		if _, err := sys.Info(); err != nil {
			t.Fatalf("info %d: %v", i, err)
		}
	}
	// 3 polls: one board read plus one system info per poll.
	if calls != 4 {
		t.Errorf("ubus calls = %d, want 4 (board cached, info live)", calls)
	}
}

// A reply carrying a zero load must not flatten the /proc reading. This is a
// defensive property, not a branch quirk: measured under load, both branches
// agree with /proc. It matters when a reply parses but carries nothing.
func TestZeroUbusLoadDoesNotOverwriteProcReading(t *testing.T) {
	v, _ := newTestVPN(t, &fakeEngine{})
	bus := ubus.NewWithRunner(func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[1] == "system" && args[2] == "info" {
			return []byte(`{"uptime":100,"load":[0,0,0],"memory":{"total":0},"root":{"total":0}}`), nil
		}
		return []byte(`{}`), nil
	})
	sys := newSystemManager(v, bus)
	sys.egress = func(func(*http.Request) (*http.Response, error)) string { return "" }
	// A stubbed /proc reading, not the real one: on a dev machine without
	// /proc/loadavg the real one is also zero, and the test would agree with a
	// broken implementation for the wrong reason.
	const fromProc = 42.0
	sys.procCPU = func() float64 { return fromProc }

	info, _ := sys.Info()
	if info.CPUPercent != fromProc {
		t.Errorf("cpu = %v, want the /proc reading %v (a zero from ubus is not news)",
			info.CPUPercent, fromProc)
	}
}

// A nonzero ubus load is authoritative: it is the device's own number, and on
// 25.12 it is the only correct one when the daemon runs in a namespace.
func TestNonZeroUbusLoadIsUsed(t *testing.T) {
	v, _ := newTestVPN(t, &fakeEngine{})
	bus := ubus.NewWithRunner(func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[1] == "system" && args[2] == "info" {
			// 65536 == a load of exactly 1.00 in the kernel's fixed point.
			return []byte(`{"uptime":100,"load":[65536,32768,16384],"memory":{"total":0},"root":{"total":0}}`), nil
		}
		return []byte(`{}`), nil
	})
	sys := newSystemManager(v, bus)
	sys.egress = func(func(*http.Request) (*http.Response, error)) string { return "" }
	// Distinct from anything ubus reports, so "the device's number won" is
	// provable rather than coincidental.
	sys.procCPU = func() float64 { return 42.0 }

	info, _ := sys.Info()
	if info.LoadAvg[0] != 1.0 || info.LoadAvg[1] != 0.5 || info.LoadAvg[2] != 0.25 {
		t.Errorf("loadAvg = %v, want [1 0.5 0.25] (fixed point / 65536)", info.LoadAvg)
	}
	if info.CPUPercent != loadToPercent(1.0) {
		t.Errorf("cpu = %v, want %v", info.CPUPercent, loadToPercent(1.0))
	}
}

// busInfoOnly answers `system board` with the given JSON and `system info`
// with the given JSON, for the shapes no real device produces.
func busPair(board, info string) *ubus.Client {
	return ubus.NewWithRunner(func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) >= 3 && args[2] == "board" {
			return []byte(board), nil
		}
		return []byte(info), nil
	})
}

// A reply that parses but carries nothing must not erase what /proc already
// said. An answered call is not the same as an informative one.
func TestEmptyUbusFieldsDoNotEraseTheProcReading(t *testing.T) {
	v, _ := newTestVPN(t, &fakeEngine{})
	sys := newSystemManager(v, busPair(`{}`, `{"uptime":0,"load":[0,0,0],"memory":{"total":0},"root":{"total":0}}`))
	sys.egress = func(func(*http.Request) (*http.Response, error)) string { return "" }
	sys.procUptime = func() int64 { return 98765 }
	sys.procCPU = func() float64 { return 42.0 }
	sys.procMem = func() (int64, int64) { return 111, 222 }

	info, err := sys.Info()
	if err != nil {
		t.Fatalf("info: %v", err)
	}
	if info.UptimeSec != 98765 {
		t.Errorf("uptime = %d, want the /proc value 98765 (ubus said 0)", info.UptimeSec)
	}
	if info.CPUPercent != 42.0 {
		t.Errorf("cpu = %v, want the /proc value 42", info.CPUPercent)
	}
	// os.Hostname() answers on every machine this test can run on, so an
	// empty hostname from ubus must not win.
	if info.Hostname == "" {
		t.Error("hostname erased by an empty ubus reply")
	}
	if info.MemUsed != 111 || info.MemTotal != 222 {
		t.Errorf("memory = %d/%d, want the /proc values 111/222 (ubus reported a total of 0)",
			info.MemUsed, info.MemTotal)
	}
}

// When ubus does name the host, that name wins: it is the one uci and the
// device's own UI show, and a disagreement with os.Hostname() is a symptom
// worth surfacing rather than hiding.
func TestUbusHostnameWinsWhenPresent(t *testing.T) {
	v, _ := newTestVPN(t, &fakeEngine{})
	sys := newSystemManager(v, busPair(`{"hostname":"cudy-router","kernel":"6.6.144"}`,
		`{"uptime":10,"load":[0,0,0],"memory":{"total":0},"root":{"total":0}}`))
	sys.egress = func(func(*http.Request) (*http.Response, error)) string { return "" }

	info, _ := sys.Info()
	if info.Hostname != "cudy-router" {
		t.Errorf("hostname = %q, want cudy-router", info.Hostname)
	}
}

// A manager built without a bus is a supported state (a dev machine, the
// userspace-netstack smoke binary, a platform that is not OpenWrt). Info()
// must degrade to /proc, not dereference a nil client.
func TestSystemInfoWithoutABus(t *testing.T) {
	v, _ := newTestVPN(t, &fakeEngine{})
	sys := newSystemManager(v, nil)
	sys.egress = func(func(*http.Request) (*http.Response, error)) string { return "" }
	sys.procUptime = func() int64 { return 4242 }
	sys.procCPU = func() float64 { return 7.5 }
	sys.procMem = func() (int64, int64) { return 111, 222 }

	info, err := sys.Info()
	if err != nil {
		t.Fatalf("info without a bus: %v", err)
	}
	if info.UptimeSec != 4242 || info.CPUPercent != 7.5 || info.MemTotal != 222 {
		t.Errorf("/proc reading lost: %+v", info)
	}
	if info.Model != "" || info.Firmware != "" {
		t.Errorf("hardware described without anything to ask: %+v", info)
	}
}

// --- the polling path must not leave the device (M2.2) ---

// Vitals is what the metrics sampler calls every few seconds. If it ever
// resolves the public address, the router starts a permanent outbound stream
// to a third party — measured on the real device before this was split out:
// conntrack showed a connection to the same public-IP service every 3s.
func TestVitalsNeverAsksTheOutsideWorld(t *testing.T) {
	v, _ := newTestVPN(t, &fakeEngine{})
	sys := newSystemManager(v, busReplay(t, "25.12", nil))
	sys.egress = func(func(*http.Request) (*http.Response, error)) string {
		t.Fatal("Vitals resolved the public IP: the polling path must not leave the device")
		return ""
	}
	sys.procCPU = func() float64 { return 3 }
	sys.procMem = func() (int64, int64) { return 1, 2 }

	got, err := sys.Vitals()
	if err != nil {
		t.Fatalf("vitals: %v", err)
	}
	// It still carries the numbers a dashboard graph needs.
	if got.MemTotal <= 0 || got.StorageUsed <= 0 {
		t.Errorf("vitals are empty: %+v", got)
	}
}

// Without a bus, Vitals degrades to /proc rather than dereferencing nil.
func TestVitalsWithoutABus(t *testing.T) {
	v, _ := newTestVPN(t, &fakeEngine{})
	sys := newSystemManager(v, nil)
	sys.egress = func(func(*http.Request) (*http.Response, error)) string {
		t.Fatal("Vitals must not touch the network")
		return ""
	}
	sys.procCPU = func() float64 { return 9 }
	sys.procMem = func() (int64, int64) { return 7, 8 }

	got, err := sys.Vitals()
	if err != nil {
		t.Fatalf("vitals: %v", err)
	}
	if got.CPUPercent != 9 || got.MemUsed != 7 || got.MemTotal != 8 {
		t.Errorf("vitals = %+v, want the /proc values", got)
	}
}

// The dashboard snapshot is read once per tab per few seconds, and resolving
// the public address is the one part of it that leaves the device. It must be
// asked for at most once per TTL, no matter how often Info() is called.
func TestWANAddressIsProbedAtMostOncePerTTL(t *testing.T) {
	v, _ := newTestVPN(t, &fakeEngine{})
	sys := newSystemManager(v, nil)
	calls := 0
	sys.egress = func(func(*http.Request) (*http.Response, error)) string {
		calls++
		return "198.51.100.7"
	}
	now := time.Unix(1_700_000_000, 0)
	sys.nowFunc = func() time.Time { return now }
	sys.wanTTL = time.Minute

	for i := 0; i < 20; i++ {
		info, _ := sys.Info()
		if info.WANIP != "198.51.100.7" {
			t.Fatalf("wanIP = %q on call %d", info.WANIP, i)
		}
	}
	if calls != 1 {
		t.Errorf("probed the outside world %d times for 20 snapshots, want 1", calls)
	}

	// Past the TTL it refreshes: a cache that never expires is a wrong answer
	// after the ISP reconnects.
	now = now.Add(2 * time.Minute)
	sys.Info()
	if calls != 2 {
		t.Errorf("probes after the TTL expired = %d, want 2", calls)
	}
}

// A failed probe must not blank a known address, and must not start the clock:
// the next call has to try again.
func TestFailedWANProbeKeepsTheLastKnownAddress(t *testing.T) {
	v, _ := newTestVPN(t, &fakeEngine{})
	sys := newSystemManager(v, nil)
	answer := "198.51.100.7"
	calls := 0
	sys.egress = func(func(*http.Request) (*http.Response, error)) string {
		calls++
		return answer
	}
	now := time.Unix(1_700_000_000, 0)
	sys.nowFunc = func() time.Time { return now }
	sys.wanTTL = time.Minute

	sys.Info() // caches 198.51.100.7
	now = now.Add(2 * time.Minute)
	answer = "" // the probe now fails
	info, _ := sys.Info()

	if info.WANIP != "198.51.100.7" {
		t.Errorf("wanIP = %q after a failed probe, want the last known address", info.WANIP)
	}
	now = now.Add(time.Second)
	answer = "198.51.100.8"
	if info, _ := sys.Info(); info.WANIP != "198.51.100.8" {
		t.Errorf("wanIP = %q, want the retry to have happened immediately", info.WANIP)
	}
}
