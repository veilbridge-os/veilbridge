package openwrt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/veilbridge-os/veilbridge/internal/core"
)

// fakeSys builds a /sys-like tree and returns a probe pointed at it. The two
// devices this project runs on disagree in both directions — the x86 stand has
// USB and no radio, the router has radios and no USB — so the shapes below are
// taken from what was actually measured on them, not invented.
type fakeSys struct {
	radios []string
	// noWiFiSubsystem omits /sys/class/ieee80211 entirely, which is what a
	// kernel with no wireless support looks like (measured on the x86 stand).
	noWiFiSubsystem bool
	netDevices      []string
	dsaOn           string   // network device carrying a DSA switch, "" for none
	swconfigSwitch  []string // entries under /sys/class/switch, the older API
	usbEntries      []string
	// tunMode: "char" points the probe at a real character device, "file" at
	// a regular file, "" leaves it absent.
	tunMode     string
	ipv6Present bool
}

func (f fakeSys) probe(t *testing.T) sysProbe {
	t.Helper()
	root := t.TempDir()

	mk := func(parts ...string) string {
		p := filepath.Join(append([]string{root}, parts...)...)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
		return p
	}

	wifi := filepath.Join(root, "sys", "class", "ieee80211")
	if !f.noWiFiSubsystem {
		mk("sys", "class", "ieee80211")
		for _, r := range f.radios {
			mk("sys", "class", "ieee80211", r)
		}
	}
	net := mk("sys", "class", "net")
	for _, d := range f.netDevices {
		mk("sys", "class", "net", d)
	}
	if f.dsaOn != "" {
		mk("sys", "class", "net", f.dsaOn, "dsa")
	}
	usb := mk("sys", "bus", "usb", "devices")
	for _, e := range f.usbEntries {
		mk("sys", "bus", "usb", "devices", e)
	}
	swtch := filepath.Join(root, "sys", "class", "switch")
	if len(f.swconfigSwitch) > 0 {
		for _, e := range f.swconfigSwitch {
			mk("sys", "class", "switch", e)
		}
	}

	p := sysProbe{
		sysClassNet:    net,
		sysClassWiFi:   wifi,
		sysBusUSB:      usb,
		sysClassSwitch: swtch,
		// Paths that must not exist unless the case says so.
		devNetTUN:   filepath.Join(root, "dev", "net", "tun"),
		procNetIPv6: filepath.Join(root, "proc", "net", "if_inet6"),
	}
	switch f.tunMode {
	case "file":
		mk("dev", "net")
		writeFile(t, p.devNetTUN, "not a device")
	case "char":
		// /dev/null is a character device that opens read-write, which is all
		// the probe asks of /dev/net/tun. Creating a real TUN node in a test
		// would need root, and a test that needs root is a test nobody runs.
		p.devNetTUN = "/dev/null"
	}
	if f.ipv6Present {
		mk("proc", "net")
		writeFile(t, p.procNetIPv6, "")
	}
	return p
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// The two real devices, as measured. If detection ever starts deciding by
// platform instead of by hardware, one of these two must go red.
func TestCapabilitiesMatchTheMeasuredDevices(t *testing.T) {
	cases := []struct {
		name string
		sys  fakeSys
		want map[string]bool
	}{
		{
			name: "x86 stand: USB controllers, no radio, no switch",
			sys: fakeSys{
				netDevices:  []string{"br-lan", "eth0", "eth1", "lo"},
				usbEntries:  []string{"usb1", "usb2", "1-0:1.0"},
				ipv6Present: true,
			},
			want: map[string]bool{
				core.CapWiFi: false, core.CapSwitchPorts: false,
				core.CapUSB: true, core.CapIPv6: true,
			},
		},
		{
			name: "router: two radios and a DSA switch, no USB port",
			sys: fakeSys{
				radios:      []string{"phy0", "phy1"},
				netDevices:  []string{"br-lan", "eth0", "lan1", "lan2", "lo", "wan"},
				dsaOn:       "eth0",
				ipv6Present: true,
			},
			want: map[string]bool{
				core.CapWiFi: true, core.CapSwitchPorts: true,
				core.CapUSB: false, core.CapIPv6: true,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			caps := tc.sys.probe(t).Capabilities()
			for name, want := range tc.want {
				if got := caps.Available(name); got != want {
					t.Errorf("%s = %v, want %v (reason %q)", name, got, want, caps[name].Reason)
				}
			}
		})
	}
}

// A device on the older swconfig API has a real managed switch. Detecting
// only DSA would report "no switch" for a reason that is about this code, not
// about the hardware - exactly what D-20 forbids.
func TestSwconfigSwitchCountsAsAManagedSwitch(t *testing.T) {
	caps := fakeSys{
		netDevices:     []string{"br-lan", "eth0", "lo"},
		swconfigSwitch: []string{"switch0"},
	}.probe(t).Capabilities()
	if !caps.Available(core.CapSwitchPorts) {
		t.Fatalf("swconfig switch not detected: %q", caps[core.CapSwitchPorts].Reason)
	}
}

// A kernel with no wireless support has no /sys/class/ieee80211 at all
// (measured on the x86 stand). That must read as "no Wi-Fi", not as a probe
// error, and the wording must not swear it is the radio that is missing when
// it could be a driver.
func TestMissingWirelessSubsystemIsNotAnError(t *testing.T) {
	caps := fakeSys{noWiFiSubsystem: true, netDevices: []string{"lo"}}.probe(t).Capabilities()
	c := caps[core.CapWiFi]
	if c.Available {
		t.Fatal("Wi-Fi reported available on a kernel with no wireless subsystem")
	}
	if !strings.Contains(c.Reason, "no Wi-Fi") {
		t.Errorf("reason = %q, want it to start from what the user cares about", c.Reason)
	}
	if !strings.Contains(c.Reason, "driver") {
		t.Errorf("reason = %q, want it to admit a driver could be the cause", c.Reason)
	}
}

// The kernel engine needs a device it can actually open, so a usable
// character device is the only thing that may answer yes.
func TestKernelTUNAcceptsAnOpenableCharacterDevice(t *testing.T) {
	caps := fakeSys{tunMode: "char"}.probe(t).Capabilities()
	if !caps.Available(core.CapKernelTUN) {
		t.Fatalf("a usable character device was rejected: %q", caps[core.CapKernelTUN].Reason)
	}
}

// D-17: a false without a reason is a blank screen with no explanation, which
// is the exact user experience this endpoint exists to prevent.
func TestEveryUnavailableCapabilityExplainsItself(t *testing.T) {
	caps := fakeSys{netDevices: []string{"lo"}}.probe(t).Capabilities()

	var off int
	for name, c := range caps {
		if c.Available {
			if c.Reason != "" {
				t.Errorf("%s is available but carries the excuse %q", name, c.Reason)
			}
			continue
		}
		off++
		if strings.TrimSpace(c.Reason) == "" {
			t.Errorf("%s is off with no reason given", name)
		}
	}
	if off == 0 {
		t.Fatal("nothing was off on a bare device, so this test proved nothing")
	}
}

// The kernel engine needs a real character device. A regular file at that path
// satisfies os.Stat and then fails every tunnel afterwards (D-34).
func TestKernelTUNRequiresACharacterDevice(t *testing.T) {
	absent := fakeSys{}.probe(t).Capabilities()
	if absent.Available(core.CapKernelTUN) {
		t.Error("kernel-tun reported available with no /dev/net/tun at all")
	}

	regular := fakeSys{tunMode: "file"}.probe(t).Capabilities()
	if regular.Available(core.CapKernelTUN) {
		t.Error("a regular file called /dev/net/tun was accepted as the TUN device")
	}
	if !strings.Contains(regular[core.CapKernelTUN].Reason, "character device") {
		t.Errorf("reason = %q, want it to name the real problem", regular[core.CapKernelTUN].Reason)
	}
}

// On the machine the tests run on, the real prober must at least answer
// without crashing and must never claim something it cannot see. This is the
// only test that touches the real filesystem, and it asserts nothing about
// this particular machine's hardware.
func TestRealProbeAnswersForEveryKnownCapability(t *testing.T) {
	caps := newSysProbe().Capabilities()
	for _, name := range []string{
		core.CapWiFi, core.CapSwitchPorts, core.CapUSB, core.CapKernelTUN, core.CapIPv6,
	} {
		if _, ok := caps[name]; !ok {
			t.Errorf("capability %q is missing from the probe's answer", name)
		}
	}
}

// An unknown name must not come back as "sure": a UI asking about a capability
// this version never probes has to degrade, not to light up.
func TestUnknownCapabilityIsNotAvailable(t *testing.T) {
	if (core.Capabilities{}).Available("wifi-7-and-a-pony") {
		t.Fatal("an unknown capability was reported as available")
	}
}
