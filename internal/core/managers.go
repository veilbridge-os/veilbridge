package core

import "errors"

// ErrNotImplemented is returned by roadmap stubs (NetworkManager, DeviceManager)
// and by any manager method not yet built in the current version. Callers and the
// API layer can detect it to surface a clean "not available in this version".
var ErrNotImplemented = errors.New("veilbridge: not implemented in this version")

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

// NetworkManager is a roadmap stub (WAN/interfaces, failover — v0.2). Its v0.1
// implementations return ErrNotImplemented. See DESIGN §4.
type NetworkManager interface {
	WANInfo() (SystemInfo, error)
}

// DeviceManager is a roadmap stub (clients/Wi-Fi/PBR binding — v0.4). Its v0.1
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
}
