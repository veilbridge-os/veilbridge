package amneziawg

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"sync"

	"github.com/amnezia-vpn/amneziawg-go/conn"
	"github.com/amnezia-vpn/amneziawg-go/device"

	netstack "github.com/veilbridge-os/veilbridge/internal/awgnetstack"
	"github.com/veilbridge-os/veilbridge/internal/vpn"
)

// netstackEngine runs the whole AmneziaWG tunnel in userspace via gVisor's
// netstack — no root, no kernel TUN. Outbound connections egress through the
// tunnel via the Dialer. This is the engine for devices without kmod-tun, for
// unprivileged runs, and for the Phase-4 smoke test. See DESIGN §5.2 and
// docs/embedding-notes.md.
//
// The product path is the kernelEngine (transparent nftables forwarding for the
// whole LAN); the adapter picks. Both satisfy vpn.Engine.
type netstackEngine struct {
	mu   sync.Mutex
	dev  *device.Device
	tnet *netstack.Net
	mtu  int
}

// NewNetstackEngine returns an Engine that tunnels in userspace.
func NewNetstackEngine() vpn.Engine {
	return &netstackEngine{mtu: 1420}
}

func (e *netstackEngine) Up(cfg vpn.NodeConfig) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.dev != nil {
		return fmt.Errorf("amneziawg: engine already up (call Down first)")
	}

	localAddr, err := parseAddrOnly(cfg.Address)
	if err != nil {
		return fmt.Errorf("amneziawg: address: %w", err)
	}
	dns, err := parseAddrs(cfg.DNS)
	if err != nil {
		return fmt.Errorf("amneziawg: dns: %w", err)
	}

	tunDev, tnet, err := netstack.CreateNetTUN([]netip.Addr{localAddr}, dns, e.mtu)
	if err != nil {
		return fmt.Errorf("amneziawg: create netstack tun: %w", err)
	}

	dev := device.NewDevice(tunDev, conn.NewDefaultBind(), device.NewLogger(device.LogLevelError, "veilbridge: "))
	uapi, err := uapiConfig(cfg)
	if err != nil {
		dev.Close()
		return fmt.Errorf("amneziawg: build uapi: %w", err)
	}
	if err := dev.IpcSet(uapi); err != nil {
		dev.Close()
		return fmt.Errorf("amneziawg: ipc set: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return fmt.Errorf("amneziawg: device up: %w", err)
	}

	e.dev = dev
	e.tnet = tnet
	return nil
}

func (e *netstackEngine) Down() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.dev == nil {
		return nil
	}
	e.dev.Close()
	e.dev = nil
	e.tnet = nil
	return nil
}

func (e *netstackEngine) Stats() (vpn.Stats, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.dev == nil {
		return vpn.Stats{HandshakeAgeSec: -1}, fmt.Errorf("amneziawg: engine down")
	}
	raw, err := e.dev.IpcGet()
	if err != nil {
		return vpn.Stats{HandshakeAgeSec: -1}, fmt.Errorf("amneziawg: ipc get: %w", err)
	}
	return parseStats(raw), nil
}

func (e *netstackEngine) Dialer() (vpn.Dialer, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.tnet == nil {
		return nil, fmt.Errorf("amneziawg: engine down")
	}
	return e.tnet, nil // *netstack.Net implements DialContext(ctx, network, address)
}

// uapiConfig renders the WireGuard/AmneziaWG UAPI config string from cfg. Keys
// arrive base64 (from the .conf) and must be hex for UAPI. Obfuscation params
// (jc, jmin, …, h1-h4) are interface-level and passed through verbatim.
func uapiConfig(cfg vpn.NodeConfig) (string, error) {
	var b strings.Builder

	priv, err := b64ToHex(cfg.PrivateKey)
	if err != nil {
		return "", fmt.Errorf("private_key: %w", err)
	}
	fmt.Fprintf(&b, "private_key=%s\n", priv)

	// Amnezia obfuscation (interface scope). Stable key order for determinism.
	for _, k := range []string{"jc", "jmin", "jmax", "s1", "s2", "s3", "s4", "h1", "h2", "h3", "h4"} {
		if v, ok := cfg.Obfuscation[k]; ok && v != "" {
			fmt.Fprintf(&b, "%s=%s\n", k, v)
		}
	}

	pub, err := b64ToHex(cfg.PeerPublicKey)
	if err != nil {
		return "", fmt.Errorf("public_key: %w", err)
	}
	fmt.Fprintf(&b, "public_key=%s\n", pub)

	if cfg.PresharedKey != "" {
		psk, err := b64ToHex(cfg.PresharedKey)
		if err != nil {
			return "", fmt.Errorf("preshared_key: %w", err)
		}
		fmt.Fprintf(&b, "preshared_key=%s\n", psk)
	}

	ep, err := resolveEndpoint(cfg.Endpoint)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(&b, "endpoint=%s\n", ep)

	if cfg.PersistentKeepalive > 0 {
		fmt.Fprintf(&b, "persistent_keepalive_interval=%d\n", cfg.PersistentKeepalive)
	}

	allowed := cfg.AllowedIPs
	if len(allowed) == 0 {
		allowed = []string{"0.0.0.0/0", "::/0"}
	}
	for _, a := range allowed {
		fmt.Fprintf(&b, "allowed_ip=%s\n", a)
	}

	return b.String(), nil
}

// parseStats extracts handshake age and rx/tx from a UAPI get dump.
func parseStats(raw string) vpn.Stats {
	st := vpn.Stats{HandshakeAgeSec: -1}
	var lastHandshakeSec int64
	for _, line := range strings.Split(raw, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "last_handshake_time_sec":
			lastHandshakeSec, _ = strconv.ParseInt(v, 10, 64)
		case "rx_bytes":
			st.RxBytes, _ = strconv.ParseInt(v, 10, 64)
		case "tx_bytes":
			st.TxBytes, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	if lastHandshakeSec > 0 {
		st.HandshakeAgeSec = int(nowUnix() - lastHandshakeSec)
		if st.HandshakeAgeSec < 0 {
			st.HandshakeAgeSec = 0
		}
	}
	return st
}

func b64ToHex(s string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return "", fmt.Errorf("not valid base64: %w", err)
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("expected 32-byte key, got %d", len(raw))
	}
	return hex.EncodeToString(raw), nil
}

func parseAddrOnly(cidrOrAddr string) (netip.Addr, error) {
	s := cidrOrAddr
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	return netip.ParseAddr(strings.TrimSpace(s))
}

func parseAddrs(list []string) ([]netip.Addr, error) {
	out := make([]netip.Addr, 0, len(list))
	for _, s := range list {
		a, err := netip.ParseAddr(strings.TrimSpace(s))
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// Compile-time check.
var _ vpn.Engine = (*netstackEngine)(nil)
