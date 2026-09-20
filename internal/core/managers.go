package core

import "errors"

// ErrNotImplemented is returned by roadmap stubs (NetworkManager, DeviceManager)
// and by any manager method not yet built in the current version. Callers and the
// API layer can detect it to surface a clean "not available in this version".
var ErrNotImplemented = errors.New("veilbridge: not implemented in this version")

// ErrNoWAN means the device has no interface with a default route: it is not a
// failure of the query but a fact about the device (an unconfigured router, or
// one whose uplink is down). Callers must tell the two apart, so it is a
// sentinel and not a zero value.
var ErrNoWAN = errors.New("veilbridge: no interface carries a default route")

// VPNManager owns VPN nodes and the active tunnel. The adapter implements it on
// top of a tunnel engine — kernel-TUN by default, userspace netstack on devices
// without kmod-tun. See DESIGN §4.
type VPNManager interface {
	// ImportConfig parses a raw AmneziaWG .conf into one or more nodes.
	ImportConfig(raw []byte) ([]Node, error)
	// ImportSubscription fetches and parses a subscription URL into nodes.
	ImportSubscription(url string) ([]Node, error)
	ListNodes() ([]Node, error)
	RemoveNode(id string) error
	// Activate brings up this node's tunnel and makes it the current egress.
	Activate(id string) error
	// Status returns the live status of one node (handshake age, rx/tx).
	Status(id string) (NodeStatus, error)
}

// RoutingManager owns the selective-routing rule set. Rules are applied
// declaratively: SetRules replaces the whole set, Apply renders it to the OS
// (nftables). See DESIGN §4, D-4.
type RoutingManager interface {
	ListRules() ([]RouteRule, error)
	// SetRules replaces the entire rule set (declarative).
	SetRules(rules []RouteRule) error
	// Apply renders the current rule set to the OS. Idempotent.
	Apply() error
}

// SystemManager exposes the dashboard snapshot, path probing, and diagnostics.
// See DESIGN §4, §6.
type SystemManager interface {
	Info() (SystemInfo, error)
	// ProbePath checks whether traffic to target goes via expected (tunnel or
	// direct), comparing egress IPs rather than trusting HTTP status. See D-5.
	ProbePath(target string, expected Target) (PathProbe, error)
	// Diagnostics runs ping/traceroute against target and returns raw output.
	Diagnostics(target string) (string, error)
}

// NetworkManager reads the device's L3 network state (M1.5). It is read-only:
// changing an interface goes through the apply transaction in M3, never
// through a getter. See DESIGN §4.
type NetworkManager interface {
	// Interfaces lists every configured L3 interface, up or not.
	Interfaces() ([]NetworkInterface, error)
	// WANInfo returns the uplink and how it was identified. It returns
	// ErrNoWAN when nothing carries a default route.
	WANInfo() (WANStatus, error)
}

// DeviceManager is a roadmap stub (clients/Wi-Fi/PBR binding — M4). Its v0.1
// implementations return ErrNotImplemented. See DESIGN §4, D-1.
type DeviceManager interface {
	ListDevices() ([]Device, error)
}

// Adapter is the per-platform bundle of managers. cmd/veilbridged detects the
// platform, constructs the matching adapter, and hands its managers to the API
// layer. The API depends only on the interfaces above, never on a concrete
// adapter. See DESIGN §1, §4.
type Adapter interface {
	// Platform reports the adapter label, e.g. "openwrt".
	Platform() string
	VPN() VPNManager
	Routing() RoutingManager
	System() SystemManager
	Network() NetworkManager
	Device() DeviceManager
	// Applier backs the apply transaction (snapshot, commit, revert). It is
	// part of the adapter and not of a manager because a transaction spans
	// every manager that writes configuration — network, firewall, wireless.
	Applier() ConfigApplier
}
