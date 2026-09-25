# internal/vpn/amneziawg

AmneziaWG VPN engine (amneziawg-go, MIT). The default and only engine in v0.1.

Two implementations of the `vpn.Engine` interface live here: `kernelEngine` (the
product path — a real `awg0` interface, so nftables can forward the whole LAN
through it; needs root and `kmod-tun`) and `netstackEngine` (userspace via the
vendored `internal/awgnetstack`, no root and no kernel TUN, the fallback for
devices without `kmod-tun` and the harness used by `cmd/p4smoke`).

Neither needs `kmod-amneziawg`: the AmneziaWG protocol runs in-process via
amneziawg-go, and the kernel only provides the TUN device. The adapter picks the
engine; the TUN type is an adapter detail. See the architecture notes in
CONTRIBUTING.md

Files:
- `parse.go` — AmneziaWG `.conf` parser → `core.Node` + `config.NodeSecret`
  (keeps Jc/Jmin/Jmax/S1-S4/H1-H4 obfuscation that Keenetic's UI hides);
  `ToNodeConfig` bridges a stored secret to `vpn.NodeConfig`.
- `kernel.go` — `kernelEngine`: creates the `awg0` TUN interface, configures
  it over UAPI; `Dialer()` is nil because the kernel routes the traffic.
- `netstack.go` — `netstackEngine`: `CreateNetTUN` → `device.NewDevice` →
  `IpcSet`; renders the UAPI config (base64 keys → hex), parses stats from
  `IpcGet`. `Dialer()` returns the netstack `*Net` (egress through the tunnel).
- `helpers.go` — endpoint resolution + time seam.

Live egress is verified for both engines — the userspace one by `cmd/p4smoke`,
the kernel one end to end on OpenWrt by `scripts/p8-e2e.sh`. Comparing their RAM
and CPU on a 256 MB router has not been done yet.
