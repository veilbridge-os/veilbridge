# internal/adapters/openwrt

The OpenWrt implementation of the core managers, and the only adapter: the
daemon refuses to start on a host without `/etc/openwrt_release` (see
`../detect.go`).

Today it uses nftables and `/proc` directly; `uci`/`ubus` arrive with the
platform layer, together with transactional apply and rollback. VeilBridge ships
its own routing and does NOT depend on podkop.

Files:

- `adapter.go` — package doc + the product constructor (kernel engine)
- `wiring.go` — the adapter struct and the engine-injecting constructor
- `vpn.go`, `routing.go`, `system.go` — one manager each
- `bind_linux.go` / `bind_other.go` — `SO_BINDTODEVICE`, with a no-op off Linux
  so the package still builds on a macOS dev machine
