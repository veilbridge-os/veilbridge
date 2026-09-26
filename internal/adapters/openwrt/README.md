# internal/adapters/openwrt

The OpenWrt implementation of the core managers, and the only adapter: the
daemon refuses to start on a host without `/etc/openwrt_release` (see
`../detect.go`).

It reads the device over `ubus` (see `ubus/`) and `/proc`/`/sys`, stages
configuration changes with `uci`, commits them only through the apply
transaction, and renders routing with nftables. VeilBridge ships its own
routing and does NOT depend on podkop.

Files:

- `adapter.go` — package doc + the product constructor, which picks the
  tunnel engine by probing the kernel (kernel TUN, or the userspace fallback)
- `wiring.go` — the adapter struct and the engine-injecting constructor
- `vpn.go`, `routing.go`, `system.go` — one manager each
- `network.go` — reading L3 interfaces and identifying the uplink
- `network_write.go` — staging uplink edits, the words the panel uses for a
  configuration key, and reading a draft back off the device
- `lan.go` / `lan_write.go` — the local network: its address, the address
  handout and the clients holding an address (M3.2)
- `firewall.go` / `firewall_write.go` / `firewall_rules.go` — the firewall
  (M3.3): zones by what they do rather than their names, forwarded ports,
  rules marked as the firewall package ships them and named by the conditions
  the panel does not show; staging port forwards and the owner's rules, with
  `fw4 check` as the last word, since it exits 0 on an entry it will skip. A
  new rule can be placed in front of another (`uci reorder` counts every
  section of the file), and one an earlier rule would always pre-empt is
  refused. Fixtures of both OpenWrt branches are in `testdata/`
- `uci.go` — the apply transaction (snapshot, commit, revert) and the
  allow-list of programs this package may execute
- `journal.go` — the on-disk apply journal, so a revert survives the daemon
  dying inside the confirmation window
- `capabilities.go` — what this device can do, probed from the kernel and
  hardware rather than from the distribution
- `ubus/` — the typed ubus client (the one seam to `/bin/ubus`), with fixtures
  captured from both supported OpenWrt branches
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
   address. `describe` returns the English words together with their stable
   key (`labelKey` in the API), and the panel translates only that key. Adding
   a phrase means regenerating `web/src/i18n/diffLabelKeys.ts`
   (`VB_UPDATE_GOLDEN=1 go test ./internal/adapters/openwrt/ -run
   TestDiffLabelKeysFileIsCurrent`) and giving it words in every locale — the
   panel does not build until it has them.
