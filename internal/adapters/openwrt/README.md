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
- `network.go` — reading L3 interfaces and identifying the uplink
- `network_write.go` — staging uplink edits, the words the panel uses for a
  configuration key, and reading a draft back off the device
- `lan.go` / `lan_write.go` — the local network: its address, the address
  handout and the clients holding an address (M3.2)
- `uci.go` — the apply transaction (snapshot, commit, revert) and the
  allow-list of programs this package may execute
- `bind_linux.go` / `bind_other.go` — `SO_BINDTODEVICE`, with a no-op off Linux
  so the package still builds on a macOS dev machine

Two rules hold across every file here, and both were paid for on real
hardware:

1. **Staging and committing are different verbs.** Everything in `*_write.go`
   writes to the uci staging area only; the transaction in `uci.go` is the one
   thing that commits, and it does so under a watchdog that restores a
   snapshot when nobody confirms the panel survived.
2. **The panel's words live in one table.** `optionLabels` in
   `network_write.go` is keyed by configuration, section ROLE and option, and
   is read both when an edit is staged and when a draft is read back off the
   device. The role matters: `ipaddr` on the uplink is the address a provider
   handed us, and the same key on the local network is this router's own
   address.
