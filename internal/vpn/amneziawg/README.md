# internal/vpn/amneziawg

AmneziaWG VPN engine (amneziawg-go, MIT). The default and only engine in v0.1.

Two implementations of the `vpn.Engine` interface will live here: `netstackEngine`
(Ubuntu/test, userspace via the vendored `internal/awgnetstack`, no root — DONE)
and `kernelEngine` (OpenWrt, kmod-amneziawg, transparent nftables forwarding —
Phase 8). The adapter picks which one based on platform; the TUN type is an
adapter detail. See the architecture notes in CONTRIBUTING.md

Files:
- `parse.go` — AmneziaWG `.conf` parser → `core.Node` + `config.NodeSecret`
  (keeps Jc/Jmin/Jmax/S1-S4/H1-H4 obfuscation that Keenetic's UI hides);
  `ToNodeConfig` bridges a stored secret to `vpn.NodeConfig`.
- `netstack.go` — `netstackEngine`: `CreateNetTUN` → `device.NewDevice` →
  `IpcSet`; renders the UAPI config (base64 keys → hex), parses stats from
  `IpcGet`. `Dialer()` returns the netstack `*Net` (egress through the tunnel).
- `helpers.go` — endpoint resolution + time seam.

Live packet flow and RAM are the Phase 4 gate, not yet verified.
