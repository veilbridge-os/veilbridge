package amneziawg

import (
	"fmt"
	"os/exec"
	"sync"

	"github.com/amnezia-vpn/amneziawg-go/conn"
	"github.com/amnezia-vpn/amneziawg-go/device"
	"github.com/amnezia-vpn/amneziawg-go/tun"

	"github.com/veilbridge-os/veilbridge/internal/vpn"
)

// kernelEngine runs the AmneziaWG tunnel on a real kernel TUN interface (awg0)
// visible to the OS. Traffic is forwarded transparently by the host's nftables
// (the routing manager), so there is no userspace Dialer — Dialer() returns nil.
// This is the engine for OpenWrt, where VeilBridge ships its own nft routing.
//
// It shares the UAPI rendering and stats parsing with netstackEngine; the only
// difference is the TUN (kernel device + OS-level addr/route) vs gVisor netstack.
// Both satisfy vpn.Engine, so the tunnel implementation is a swappable detail:
// this one needs kmod-tun and root, the netstack one needs neither.
type kernelEngine struct {
	mu   sync.Mutex
	dev  *device.Device
	mtu  int
	name string // interface name, e.g. "awg0"
	// addr/allowed retained so Down can be a clean no-op even if link teardown
	// is implicit (closing the TUN removes the interface and its routes).
	addr string
}

// NewKernelEngine returns an Engine that tunnels via a kernel TUN interface.
func NewKernelEngine() vpn.Engine {
	// The package's firewall include states the MSS for this MTU and matches
	// this name (packaging/openwrt/usr/share/nftables.d): change them together.
	return &kernelEngine{mtu: 1420, name: "awg0"}
}

func (e *kernelEngine) Up(cfg vpn.NodeConfig) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.dev != nil {
		return fmt.Errorf("amneziawg: kernel engine already up (call Down first)")
	}

	if _, err := parseAddrOnly(cfg.Address); err != nil {
		return fmt.Errorf("amneziawg: address: %w", err)
	}

	tunDev, err := tun.CreateTUN(e.name, e.mtu)
	if err != nil {
		return fmt.Errorf("amneziawg: create kernel tun %q (need root + /dev/net/tun): %w", e.name, err)
	}
	// The kernel may hand back a different name (rare on Linux); honor it.
	if real, err := tunDev.Name(); err == nil && real != "" {
		e.name = real
	}

	dev := device.NewDevice(tunDev, conn.NewDefaultBind(), device.NewLogger(device.LogLevelError, "veilbridge: "))
	uapi, err := uapiConfig(cfg)
	if err != nil {
		dev.Close()
		return fmt.Errorf("amneziawg: build uapi: %w", err)
	}
	// Mark the encrypted socket so it is routed outside the tunnel (see
	// vpn.BypassMark). An interface-level key, so it goes before the peer.
	uapi = fmt.Sprintf("fwmark=%d\n", vpn.BypassMark) + uapi
	if err := dev.IpcSet(uapi); err != nil {
		dev.Close()
		return fmt.Errorf("amneziawg: ipc set: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return fmt.Errorf("amneziawg: device up: %w", err)
	}

	// Bring the OS interface up with its tunnel address. `ip` is present on
	// OpenWrt (busybox or ip-full), so this stays portable across targets
	// instead of binding to a netlink library.
	if err := e.configureLink(cfg); err != nil {
		dev.Close()
		return fmt.Errorf("amneziawg: configure link: %w", err)
	}

	e.dev = dev
	e.addr = cfg.Address
	return nil
}

// configureLink assigns the tunnel address and brings the interface up. It
// routes nothing: the connection's networks go into its own table, which the
// OS adapter owns (D-73, D-82). Writing them into the main table took the
// table away from netifd and made marked traffic follow the uplink (#47).
func (e *kernelEngine) configureLink(cfg vpn.NodeConfig) error {
	if err := ipCmd("addr", "add", cfg.Address, "dev", e.name); err != nil {
		return err
	}
	return ipCmd("link", "set", "up", "dev", e.name)
}

func (e *kernelEngine) Down() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.dev == nil {
		return nil
	}
	// Closing the device removes the kernel TUN, which drops its addr and routes.
	e.dev.Close()
	e.dev = nil
	e.addr = ""
	return nil
}

func (e *kernelEngine) Stats() (vpn.Stats, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.dev == nil {
		return vpn.Stats{HandshakeAgeSec: -1}, fmt.Errorf("amneziawg: kernel engine down")
	}
	raw, err := e.dev.IpcGet()
	if err != nil {
		return vpn.Stats{HandshakeAgeSec: -1}, fmt.Errorf("amneziawg: ipc get: %w", err)
	}
	return parseStats(raw), nil
}

// Dialer returns nil: the kernel engine forwards traffic transparently via
// nftables, there is no in-process tunnel socket to dial through (DESIGN §5.2).
func (e *kernelEngine) Dialer() (vpn.Dialer, error) {
	return nil, nil
}

// TunName reports the kernel TUN interface name while the tunnel is up ("" when
// down). The system manager uses it to verify egress for kernel engines, where
// there is no Dialer to probe through — it binds a socket to this interface
// instead. Satisfies vpn.InterfaceEngine.
func (e *kernelEngine) TunName() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.dev == nil {
		return ""
	}
	return e.name
}

// ipCmd runs `ip <args...>`, surfacing the command output on failure.
func ipCmd(args ...string) error {
	out, err := exec.Command("ip", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ip %v: %w: %s", args, err, string(out))
	}
	return nil
}

// Compile-time check.
var _ vpn.Engine = (*kernelEngine)(nil)
