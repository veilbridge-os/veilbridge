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
// Both satisfy vpn.Engine, which is the whole point of Phase 8 (NFR-1: the same
// code runs on both platforms, the adapter just picks the engine).
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
	if err := dev.IpcSet(uapi); err != nil {
		dev.Close()
		return fmt.Errorf("amneziawg: ipc set: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return fmt.Errorf("amneziawg: device up: %w", err)
	}

	// Bring the OS interface up with its tunnel address and route the AllowedIPs
	// into it. `ip` is present on both OpenWrt (busybox/ip-full) and Ubuntu, so
	// this stays portable instead of binding to a netlink library per platform.
	if err := e.configureLink(cfg); err != nil {
		dev.Close()
		return fmt.Errorf("amneziawg: configure link: %w", err)
	}

	e.dev = dev
	e.addr = cfg.Address
	return nil
}

// configureLink assigns the tunnel address and AllowedIPs routes to the kernel
// interface. The endpoint route (so the encrypted UDP itself doesn't recurse
// into the tunnel) is the routing manager's job, not the engine's.
func (e *kernelEngine) configureLink(cfg vpn.NodeConfig) error {
	if err := ipCmd("addr", "add", cfg.Address, "dev", e.name); err != nil {
		return err
	}
	if err := ipCmd("link", "set", "up", "dev", e.name); err != nil {
		return err
	}
	allowed := cfg.AllowedIPs
	if len(allowed) == 0 {
		allowed = []string{"0.0.0.0/0", "::/0"}
	}
	for _, a := range allowed {
		// A /0 default would hijack the host's own egress; transparent routing
		// of client traffic is the routing manager's concern. The engine only
		// needs the link reachable, so skip catch-all routes here.
		if a == "0.0.0.0/0" || a == "::/0" {
			continue
		}
		if err := ipCmd("route", "replace", a, "dev", e.name); err != nil {
			return err
		}
	}
	return nil
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
