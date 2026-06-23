// Package vpn defines the engine abstraction that hides the TUN type (userspace
// netstack vs kernel-TUN) behind one interface, chosen by the OS adapter. See
// CONTRIBUTING.md §5 (D-2). The concrete AmneziaWG engines live in the
// internal/vpn/amneziawg subpackage; Xray (v0.3) will implement the same Engine.
package vpn

import (
	"context"
	"net"
)

// NodeConfig is everything an engine needs to bring one tunnel up. For AmneziaWG
// these map to WireGuard UAPI keys plus the Amnezia obfuscation params. See
// DESIGN §5.2 and internal/config.NodeSecret.
type NodeConfig struct {
	// PrivateKey of this peer (base64).
	PrivateKey string
	// PeerPublicKey of the server (base64).
	PeerPublicKey string
	// PresharedKey is optional (base64).
	PresharedKey string
	// Endpoint is the server host:port (e.g. "203.0.113.10:443").
	Endpoint string
	// Address is the tunnel-internal address, e.g. "10.8.1.2/32".
	Address string
	// DNS servers advertised by the tunnel.
	DNS []string
	// AllowedIPs routed into the tunnel.
	AllowedIPs []string
	// PersistentKeepalive in seconds (0 = off).
	PersistentKeepalive int
	// Obfuscation holds AmneziaWG UAPI params (jc, jmin, jmax, s1-s4, h1-h4),
	// passed through opaquely to the engine.
	Obfuscation map[string]string
}

// Dialer makes outbound connections that egress through the tunnel. The
// userspace (netstack) engine returns a real Dialer; the kernel engine returns
// nil from Engine.Dialer because traffic is forwarded transparently by nftables
// (DESIGN §5.2).
type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// Stats is a point-in-time view of a tunnel.
type Stats struct {
	// HandshakeAgeSec is seconds since the last handshake; -1 means never.
	// This is the source of truth for liveness (D-5).
	HandshakeAgeSec int
	RxBytes         int64
	TxBytes         int64
}

// Engine drives a single VPN tunnel. One process may run one Engine at a time
// (the active node). Implementations: netstackEngine (userspace, Ubuntu/test)
// and kernelEngine (OpenWrt). See DESIGN §5.2.
type Engine interface {
	// Up brings the tunnel up with cfg. It is an error to call Up twice without
	// an intervening Down.
	Up(cfg NodeConfig) error
	// Down tears the tunnel down. Safe to call when already down.
	Down() error
	// Stats returns current tunnel statistics.
	Stats() (Stats, error)
	// Dialer returns a Dialer that egresses through the tunnel, or nil for
	// kernel engines where forwarding is transparent.
	Dialer() (Dialer, error)
}
