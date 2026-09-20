package openwrt

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/veilbridge-os/veilbridge/internal/core"
)

// Capability detection asks the kernel, not the distribution: every probe here
// reads something the hardware either exposes or does not. That is what makes
// D-20 enforceable — "off" always has a physical answer behind it.
//
// Measured on the two stands, and they disagree in both directions, which is
// exactly why this exists:
//
//	                 vb-openwrt (x86 VM, 23.05)   Cudy WR3000S (25.12)
//	wifi             no radio                     phy0, phy1
//	usb              4 QEMU controllers           none — the board has no port
//	switch-ports     none                         DSA switch on eth0
//
// A panel that decided by platform would get both devices wrong.

// sysProbe reads the filesystem to answer capability questions. The roots are
// fields rather than constants so tests can point them at a fabricated tree:
// the alternative is a test that only passes on the machine it was written on.
type sysProbe struct {
	// sysClassNet is /sys/class/net — one directory per network device.
	sysClassNet string
	// sysClassWiFi is /sys/class/ieee80211 — one directory per radio. Note
	// this is NOT the ubus "iwinfo" object: that object exists on a device
	// with no radio at all (verified on the x86 stand), so asking ubus would
	// report Wi-Fi on a machine that has none.
	sysClassWiFi string
	// sysBusUSB is /sys/bus/usb/devices — empty on a board without a
	// controller.
	sysBusUSB string
	// sysClassSwitch is /sys/class/switch, where the older swconfig drivers
	// register. DSA replaced them on the branches we ship, but a device still
	// using swconfig has a real switch, and calling that "no switch" would be
	// a limitation of this code dressed up as a physical fact (D-20).
	sysClassSwitch string
	// devNetTUN is /dev/net/tun, the kernel engine's requirement (D-34).
	devNetTUN string
	// procNetIPv6 is /proc/net/if_inet6, absent when IPv6 is not compiled in.
	procNetIPv6 string
}

func newSysProbe() sysProbe {
	return sysProbe{
		sysClassNet:    "/sys/class/net",
		sysClassWiFi:   "/sys/class/ieee80211",
		sysBusUSB:      "/sys/bus/usb/devices",
		sysClassSwitch: "/sys/class/switch",
		devNetTUN:      "/dev/net/tun",
		procNetIPv6:    "/proc/net/if_inet6",
	}
}

// Capabilities probes the hardware. Called once at start up; see
// core.CapabilityProbe for why not per request.
func (p sysProbe) Capabilities() core.Capabilities {
	return core.Capabilities{
		core.CapWiFi:        p.wifi(),
		core.CapSwitchPorts: p.switchPorts(),
		core.CapUSB:         p.usb(),
		core.CapKernelTUN:   p.kernelTUN(),
		core.CapIPv6:        p.ipv6(),
	}
}

func (p sysProbe) wifi() core.Capability {
	radios, err := listDir(p.sysClassWiFi)
	switch {
	// Both branches mean the same thing to a person - there is no Wi-Fi here -
	// so both say that first and keep the technical detail as a hint for
	// whoever has to fix it.
	case err != nil:
		// No wireless subsystem at all. Usually no radio; it can also be a
		// driver that never loaded, so the wording does not swear about which.
		return core.Capability{Reason: "no Wi-Fi available: this kernel has no wireless subsystem (no radio, or its driver did not load)"}
	case len(radios) == 0:
		return core.Capability{Reason: "no Wi-Fi radio on this device"}
	default:
		return core.Capability{Available: true}
	}
}

// switchPorts looks for a DSA switch: the kernel marks a conduit interface
// with a `dsa` directory, and its ports appear as separate netdevs. The old
// swconfig API is deliberately not probed — it is gone from the branches we
// support, and a capability nobody can act on is worse than an honest "no".
func (p sysProbe) switchPorts() core.Capability {
	devices, err := listDir(p.sysClassNet)
	if err != nil {
		return core.Capability{Reason: fmt.Sprintf("cannot read %s", p.sysClassNet)}
	}
	for _, dev := range devices {
		if isDir(filepath.Join(p.sysClassNet, dev, "dsa")) {
			return core.Capability{Available: true}
		}
	}
	// swconfig is the older API, still alive on ath79 and ramips devices; its
	// drivers register under /sys/class/switch. Looking only for DSA would
	// report "no switch" on hardware that has one, which is a limitation of
	// this code, not a physical fact - and D-20 allows only the latter.
	if switches, err := listDir(p.sysClassSwitch); err == nil && len(switches) > 0 {
		return core.Capability{Available: true}
	}
	return core.Capability{Reason: "no managed switch on this device: its ports cannot be controlled separately"}
}

// usb reports whether the board has a USB controller - not whether anything
// is plugged into it. A root hub with no devices still means a port exists,
// which is the question the UI needs answered before offering storage or
// modem sections at all.
func (p sysProbe) usb() core.Capability {
	entries, err := listDir(p.sysBusUSB)
	switch {
	case err != nil:
		return core.Capability{Reason: "no USB port on this device (this kernel has no USB support)"}
	case len(entries) == 0:
		return core.Capability{Reason: "no USB controller on this device"}
	default:
		return core.Capability{Available: true}
	}
}

// kernelTUN decides between the kernel engine and the userspace netstack
// (D-34). Presence is not enough on three counts, so all three are checked:
// a plain file at that path would satisfy os.Stat; a character node left
// behind by an unloaded module is still there but no longer works; and a
// daemon without permission on it will fail at the first tunnel, not here.
// Opening it once at start up answers all three for the price of one syscall.
func (p sysProbe) kernelTUN() core.Capability {
	info, err := os.Stat(p.devNetTUN)
	if err != nil {
		return core.Capability{Reason: "no /dev/net/tun: install kmod-tun to use the kernel engine"}
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		return core.Capability{Reason: "/dev/net/tun exists but is not a character device"}
	}
	f, err := os.OpenFile(p.devNetTUN, os.O_RDWR, 0)
	if err != nil {
		return core.Capability{Reason: fmt.Sprintf("/dev/net/tun cannot be opened: %v", err)}
	}
	_ = f.Close()
	return core.Capability{Available: true}
}

func (p sysProbe) ipv6() core.Capability {
	if _, err := os.Stat(p.procNetIPv6); err != nil {
		return core.Capability{Reason: "no IPv6 on this device: the kernel was built without it"}
	}
	return core.Capability{Available: true}
}

func listDir(path string) ([]string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names, nil
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
