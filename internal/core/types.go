// Package core defines VeilBridge's domain model and the abstract manager
// interfaces that platform adapters implement. It depends on nothing but the
// standard library — no OS specifics, no concrete adapters, no engine packages.
//
// The dependency rule is one-way: the API layer and adapters both point at
// core, never the other way around. See the architecture notes in CONTRIBUTING.md
package core

// EngineKind identifies a VPN engine. v0.1 ships amneziawg only; xray
// (VLESS/REALITY) arrives later as a market app (roadmap M10). See DESIGN §2.
type EngineKind string

const (
	EngineAmneziaWG EngineKind = "amneziawg"
	EngineXray      EngineKind = "xray"
)

// TunnelEngineKernel / TunnelEngineUserspace name the engine actually driving
// tunnels on this device (D-34, M1.7). The kernel engine needs /dev/net/tun;
// where that is absent the userspace netstack takes over, and the panel says
// so rather than pretending the two are the same thing: only the kernel engine
// can forward traffic for the whole LAN.
const (
	TunnelEngineKernel    = "kernel"
	TunnelEngineUserspace = "userspace"
)

// Target is where traffic to a rule's value should go.
type Target string

const (
	TargetTunnel Target = "tunnel"
	TargetDirect Target = "direct"
)

// RuleKind distinguishes how a RouteRule matches traffic.
type RuleKind string

const (
	RuleDomain RuleKind = "domain"
	RuleSubnet RuleKind = "subnet"
)

// Node is a VPN exit point (NL/FI/DE…). Secret engine parameters (private keys,
// peer keys, obfuscation) are NOT held here — they live in the config store and
// never cross the API boundary. See DESIGN §2, D-3.
type Node struct {
	// ID is stable, derived from the peer public key.
	ID string `json:"id"`
	// Name is human-readable, e.g. "NetherlandsAWG".
	Name string `json:"name"`
	// Engine selects which VPN engine drives this node.
	Engine EngineKind `json:"engine"`
	// Endpoint is host:port, e.g. "203.0.113.10:443".
	Endpoint string `json:"endpoint"`
}

// NodeStatus is the live state of a node. HandshakeAgeSec is the source of
// truth for tunnel liveness (DESIGN §2, D-5) — not a ping, not an HTTP 200.
type NodeStatus struct {
	NodeID string `json:"nodeId"`
	// Active is true when this node is the current egress.
	Active bool `json:"active"`
	// HandshakeAgeSec is seconds since the last handshake; -1 means never.
	HandshakeAgeSec int `json:"handshakeAgeSec"`
	// LastError holds the most recent error string, if any.
	LastError string `json:"lastError,omitempty"`
	RxBytes   int64  `json:"rxBytes"`
	TxBytes   int64  `json:"txBytes"`
}

// NodeWithStatus pairs a node with its latest status for list responses.
type NodeWithStatus struct {
	Node   Node        `json:"node"`
	Status *NodeStatus `json:"status,omitempty"`
}

// RouteRule decides what to send through the tunnel vs directly. See DESIGN §2.
type RouteRule struct {
	ID string `json:"id"`
	// Kind is "domain" or "subnet".
	Kind RuleKind `json:"kind"`
	// Value is a domain ("youtube.com") or CIDR ("128.116.0.0/17").
	Value string `json:"value"`
	// Target is "tunnel" or "direct".
	Target Target `json:"target"`
	// Note is a free-form user comment.
	Note string `json:"note,omitempty"`
}

// SystemInfo is the dashboard snapshot (FR-4). TunnelUp reflects whether traffic
// actually flows through the tunnel, verified rather than assumed. See DESIGN §2.
type SystemInfo struct {
	// Platform is the detected adapter's label, e.g. "openwrt" ("mock" in -demo).
	Platform string `json:"platform"`
	Hostname string `json:"hostname"`
	// Model is what the board calls itself, e.g. "Cudy WR3000S v1". Empty when
	// the platform cannot name its hardware — a PC, or a container.
	Model string `json:"model,omitempty"`
	// Firmware is the running release, e.g. "OpenWrt 24.10.8 r28568-…".
	Firmware string `json:"firmware,omitempty"`
	// Kernel is the running kernel version.
	Kernel     string  `json:"kernel,omitempty"`
	UptimeSec  int64   `json:"uptimeSec"`
	CPUPercent float64 `json:"cpuPercent"`
	// LoadAvg is the 1/5/15-minute load average. CPUPercent is derived from it
	// and is what a dashboard shows; the raw triple is kept because on a
	// two-core router the difference between 1.0 and 4.0 is the whole story.
	LoadAvg  [3]float64 `json:"loadAvg"`
	MemUsed  int64      `json:"memUsed"`
	MemTotal int64      `json:"memTotal"`
	// StorageUsed/StorageTotal describe the writable root (the overlay on a
	// flash router), in bytes. It is reported because on a device with ~45 MB
	// of overlay a full filesystem is a likelier outage than a busy CPU.
	StorageUsed  int64 `json:"storageUsed,omitempty"`
	StorageTotal int64 `json:"storageTotal,omitempty"`
	// WANIP is the direct egress (e.g. the RU ISP IP).
	WANIP string `json:"wanIP,omitempty"`
	// EgressIP is the actual egress through the active node.
	EgressIP string `json:"egressIP,omitempty"`
	// EgressGeo is human-readable, e.g. "NL / Amsterdam".
	EgressGeo string `json:"egressGeo,omitempty"`
	// TunnelUp reports whether traffic genuinely flows through the tunnel.
	TunnelUp bool `json:"tunnelUp"`
	// TunnelEngine is the engine in use: "kernel" or "userspace". It is the
	// daemon's actual choice, not a preference, and it is reported because the
	// two differ in what they can do for the LAN behind the router.
	TunnelEngine string `json:"tunnelEngine,omitempty"`
}

// PathProbe is the result of checking whether traffic to a target goes through
// the tunnel or directly. It compares egress IPs; it does NOT trust HTTP status.
// This encodes the "curl 200 ≠ tunnel" lesson. See DESIGN §6, D-5.
type PathProbe struct {
	Target string `json:"target"`
	// ExpectedVia is the path the caller expected (tunnel or direct).
	ExpectedVia Target `json:"expectedVia"`
	// ActualVia is where traffic actually went.
	ActualVia Target `json:"actualVia"`
	// OK is true when ActualVia == ExpectedVia.
	OK bool `json:"ok"`
	// Detail carries the egress IP, timing, and method used.
	Detail string `json:"detail,omitempty"`
}

// NetworkInterface is one L3 interface as the platform's network daemon sees
// it (on OpenWrt: one entry of `ubus call network.interface dump`). It is a
// read model — M3 owns changing any of it.
type NetworkInterface struct {
	// Name is the logical name ("wan", "lan"), not the kernel device.
	Name string `json:"name"`
	// Device is the kernel device traffic actually leaves through ("br-lan",
	// "eth1"). For a bridge or a tunnel it differs from the logical name, and
	// this is the one to bind a socket or read counters on.
	Device string `json:"device,omitempty"`
	// Up is true when the interface is configured and carrying an address.
	Up bool `json:"up"`
	// Proto is how it gets its address: "dhcp", "static", "pppoe", …
	Proto string `json:"proto,omitempty"`
	// UptimeSec is how long this interface has been up, not the system uptime.
	UptimeSec int64 `json:"uptimeSec,omitempty"`
	// IPv4 and IPv6 are assigned addresses in CIDR form.
	IPv4 []string `json:"ipv4,omitempty"`
	IPv6 []string `json:"ipv6,omitempty"`
	// Gateway/Gateway6 are the default-route nexthops, empty when this
	// interface has no default route.
	Gateway  string `json:"gateway,omitempty"`
	Gateway6 string `json:"gateway6,omitempty"`
	// DNS are the resolvers this interface learned or was given.
	DNS []string `json:"dns,omitempty"`
}

// HasDefaultRoute reports whether this interface carries a default route, i.e.
// whether traffic for the rest of the world can leave through it.
func (i NetworkInterface) HasDefaultRoute() bool {
	return i.Gateway != "" || i.Gateway6 != ""
}

// WANStatus is the uplink, plus how it was identified. The "how" is part of
// the answer on purpose: a router can have several default routes (a VM stand
// here has two), and a panel that silently picks one of them teaches the
// operator to trust a guess. See D-5 — report what was measured, not what was
// assumed.
type WANStatus struct {
	Interface NetworkInterface `json:"interface"`
	// SelectedBy names the rule that picked it: "default-route" (it was the
	// only candidate), "name" (several candidates, one is called wan), or
	// "first-candidate" (several, none named wan — an honest coin toss).
	SelectedBy string `json:"selectedBy"`
	// Candidates lists every interface with a default route, including the
	// selected one, so the UI can show the ambiguity instead of hiding it.
	Candidates []string `json:"candidates,omitempty"`
}

// Device is a LAN client. v0.1 only ever returns these from a stub; full device
// management (Wi-Fi, PBR binding) is roadmap M4. See DESIGN §4, D-1.
type Device struct {
	MAC      string `json:"mac"`
	IP       string `json:"ip"`
	Hostname string `json:"hostname,omitempty"`
}
