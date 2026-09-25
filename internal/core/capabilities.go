package core

// Capabilities are how the UI learns what this particular device can do. The
// front end never branches on the platform (D-3, D-17): it asks what is
// available and renders accordingly, so a router with no radio shows "no Wi-Fi
// adapter on this device" instead of an empty screen or a section that throws.
//
// The rule that keeps this honest is D-20: a capability is false ONLY for a
// physical reason — no radio, no USB, no switch, no kernel module, not enough
// flash. "We have not built it yet" is never a capability; that is an open
// milestone, and pretending otherwise turns this map into a landfill of
// unfinished work.

// Capability names. They are constants because they are a contract with the
// UI: a typo in a string literal would silently disable a whole section.
const (
	// CapWiFi: the device has at least one radio.
	CapWiFi = "wifi"
	// CapSwitchPorts: the device has a switch with per-port control.
	CapSwitchPorts = "switch-ports"
	// CapUSB: the device has a USB controller to attach storage or a modem.
	CapUSB = "usb"
	// CapKernelTUN: /dev/net/tun exists, so tunnels can use the kernel engine
	// instead of the userspace netstack (D-34).
	CapKernelTUN = "kernel-tun"
	// CapIPv6: the kernel has IPv6 support compiled in.
	CapIPv6 = "ipv6"
	// CapDHCPServer: the device can have a local network to hand addresses
	// out on — a second network port, or a radio to be an access point. A box
	// with one port and no radio has nobody to serve, which is a shape of
	// hardware and not a missing feature (D-20, #34).
	CapDHCPServer = "dhcp-server"
)

// Capability is one answer: can we, and if not, why not.
type Capability struct {
	Available bool `json:"available"`
	// Reason explains a false in words a human can act on, and in the panel's
	// own vocabulary: no device nodes, no package names, no kernel modules.
	// It is empty when Available is true — there is nothing to explain about
	// something that works.
	//
	// The split from Detail exists because the two rules pull in opposite
	// directions: D-17 says the UI must show WHY something is off, and D-3
	// says the UI must not speak in OS terms. One string cannot honour both,
	// so there are two, and the interface decides which one it shows first.
	Reason string `json:"reason,omitempty"`
	// Detail is the same answer for whoever has to fix it: the device node,
	// the module, the errno. It belongs behind a "technical details"
	// disclosure — the same place a diagnostics screen shows raw output —
	// never in the sentence the user reads first.
	Detail string `json:"detail,omitempty"`
}

// Capabilities maps a capability name to its answer.
type Capabilities map[string]Capability

// Available reports whether a named capability is usable. An unknown name is
// not available: a UI asking about something this version never probes must
// not get an optimistic yes.
func (c Capabilities) Available(name string) bool {
	entry, ok := c[name]
	return ok && entry.Available
}

// CapabilityProbe is implemented by adapters that can report what the hardware
// they run on is able to do.
type CapabilityProbe interface {
	// Capabilities returns the detected set. Detection happens once, at start
	// up: probing on every request would put a filesystem walk behind a poll
	// that the dashboard makes every few seconds.
	Capabilities() Capabilities
}
