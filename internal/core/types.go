// Package core defines VeilBridge's domain model and the abstract manager
// interfaces that platform adapters implement. It depends on nothing but the
// standard library — no OS specifics, no concrete adapters, no engine packages.
//
// The dependency rule is one-way: the API layer and adapters both point at
// core, never the other way around. See the architecture notes in CONTRIBUTING.md
package core

// EngineKind identifies a VPN engine. v0.1 ships amneziawg only; xray
// (VLESS/REALITY) arrives in v0.3. See DESIGN §2.
type EngineKind string

const (
	EngineAmneziaWG EngineKind = "amneziawg"
	EngineXray      EngineKind = "xray"
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
	Platform   string  `json:"platform"`
	Hostname   string  `json:"hostname"`
	UptimeSec  int64   `json:"uptimeSec"`
	CPUPercent float64 `json:"cpuPercent"`
	MemUsed    int64   `json:"memUsed"`
	MemTotal   int64   `json:"memTotal"`
	// WANIP is the direct egress (e.g. the RU ISP IP).
	WANIP string `json:"wanIP,omitempty"`
	// EgressIP is the actual egress through the active node.
	EgressIP string `json:"egressIP,omitempty"`
	// EgressGeo is human-readable, e.g. "NL / Amsterdam".
	EgressGeo string `json:"egressGeo,omitempty"`
	// TunnelUp reports whether traffic genuinely flows through the tunnel.
	TunnelUp bool `json:"tunnelUp"`
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

// Device is a LAN client. v0.1 only ever returns these from a stub; full device
// management (Wi-Fi, PBR binding) is roadmap v0.4. See DESIGN §4, D-1.
type Device struct {
	MAC      string `json:"mac"`
	IP       string `json:"ip"`
	Hostname string `json:"hostname,omitempty"`
}
