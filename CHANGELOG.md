# Changelog

What changed for a person running VeilBridge on a router. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and versions follow
[semantic versioning](https://semver.org/); how releases are planned and cut is
in [`RELEASING.md`](./RELEASING.md). Each release also has written notes in
[`docs/release-notes/`](./docs/release-notes/).

## [Unreleased]

### Added
- **A way back after a network change that stuck**
  ([#38](https://github.com/veilbridge-os/veilbridge/issues/38)):
  `veilbridged -restore-network` puts the network, firewall and
  address-handout settings back from before the last change that stuck —
  changes the router undid by itself are skipped — and leaves everything else
  alone. `-list-restore-points` shows what each restore point would change,
  `-restore-point <id>` goes further back, and the settings being replaced
  become a restore point themselves, so a restore can be undone. While a change
  still waits for confirmation it refuses and says when the router will undo
  it (`-force` to override). Works with no network: over ssh, on a console,
  in OpenWrt's failsafe mode after `mount_root`. Instructions:
  [`docs/emergency-access.md`](./docs/emergency-access.md). Verified on the
  router over ssh and on the VM from its console and from failsafe mode, each
  time after a confirmed change that had cut the way in.
- Static routes through the API (`GET`/`PUT /network/routes`,
  `DELETE /network/routes/{id}`): add, edit, switch off and remove, staged and
  applied under the confirmation window. Each route says whether the kernel is
  actually using it — the router's own status lists routes the kernel refused.
  Refused before they reach the router: a gateway outside the chosen
  connection's network (the router would accept the route and never use it),
  and a network and metric the router already routes — a connection's own
  network, another route, or a route of the tunnel — because the network
  service would take that route over and remove it together with the new one.
  A route written in the older address-plus-mask form keeps its form when
  edited ([#37](https://github.com/veilbridge-os/veilbridge/issues/37)).
  Verified on both stands: a route that cut the panel off came back by itself
  when nobody confirmed; an unrelated route of somebody else's survived adding,
  editing, switching off and removing ours.
- **The static routes screen** (menu: Network rules → Static routes). Every
  route's state comes first: working, off, not working — with the reason when
  it can be told ("the gateway is outside the network of the connection") —
  or not checked, for a route in its own table. Add a route to a network or to
  a single host; the connection is picked by the gateway. A network the router
  already routes is refused with a one-click "Set metric 10"; a route with
  settings the panel does not show can only be switched on and off or removed;
  IPv6 routes are listed read-only
  ([#48](https://github.com/veilbridge-os/veilbridge/issues/48)). Verified on
  the router through the screen itself: both refusals at their fields, the
  one-click fix, a route confirmed and working in the kernel next to another
  program's route that survived its removal, and a route cutting the panel off
  that came back by itself with the screen saying so.

### Known issues
- Subnet rules "through the tunnel" (since v0.1) mark the traffic, but nothing
  routes the mark, so the traffic does not go through the tunnel
  ([#47](https://github.com/veilbridge-os/veilbridge/issues/47)).

## [0.2.0-alpha2] — 2026-09-26 — pre-release

The second build of the `v0.2` line, still a **pre-release**: static routes
and emergency access are to come, and it is never offered as the latest
release. The firewall is in; so are fixes for three defects of `alpha1`.

### Added
- `GET /firewall`: zones with their role (internet side, local network —
  derived from what a zone does, not from its name), which zone may reach
  which, forwarded ports and traffic rules, with the rules the firewall
  package ships marked as the system's (read-only in the panel). Reading only;
  changes follow ([#35](https://github.com/veilbridge-os/veilbridge/issues/35)).
- Port forwards through the API: add, edit, switch off and remove, staged and
  applied under the confirmation window like every firewall change. The
  device's own firewall checks the draft before it is accepted — it silently
  skips an entry it cannot use, so its warnings are treated as a refusal.
  Verified on the router: a forwarded port answers from outside, closes by
  itself when nobody confirms, and closes again when removed.
- Your own firewall rules through the API (`PUT /firewall/rules`,
  `DELETE /firewall/rules/{id}`): allow or block traffic to the router or
  through it, by zone, protocol, ports (one, a range, or a list) and IP
  version; add, edit, switch off, remove. The rules the firewall ships stay
  read-only. **Order matters** — the first matching rule wins — so a new rule
  can be placed in front of an existing one, and a rule that could never act
  where it would land (an earlier rule already decides the same traffic the
  other way) is refused with that rule's name instead of being accepted and
  silently doing nothing. A rule with conditions the panel does not show yet
  (source address, schedule, …) names them, and can be switched on and off or
  removed but not edited here
  ([#35](https://github.com/veilbridge-os/veilbridge/issues/35)). Verified on
  the router: a rule blocking the panel from outside, placed in front of the
  rule that allows it, closed the panel and the settings came back by
  themselves when nobody confirmed.
- Move one of your own rules in the list (`POST /firewall/rules/{id}/move`):
  in front of another rule, or to the end. The move is its own row in the
  list of changes — "Place in the list — Game console: No. 11 → No. 10" —
  both right away and after a reload; before, a reorder would have been an
  invisible change. A move that would put the rule where an earlier rule
  always decides first is refused, naming that rule. Firmware rules do not
  move, but your rule can go above them
  ([#46](https://github.com/veilbridge-os/veilbridge/issues/46)). Verified on
  the router: moving a block of the panel in front of the rule that allows it
  closed the panel, and the order came back by itself when nobody confirmed.
- A `dhcp-server` capability: a device with one network port and no Wi-Fi has
  no local network to hand addresses out on, says so with a reason, and the
  panel does not show a local network section there at all
  ([#34](https://github.com/veilbridge-os/veilbridge/issues/34)).

- **The firewall screen** (menu: Network rules → Firewall). What is open
  from the internet, first and in one sentence; port forwards with the device
  picked from the devices on the network; your own rules, numbered as the
  device runs them, switched on and off, moved by dragging or with arrows;
  firmware rules folded, read-only; zones as a reference. A rule that could
  never act where it would land is refused next to "Place in the list", with a
  one-click "Put it before …". Every change goes through the apply bar under
  the confirmation window
  ([#36](https://github.com/veilbridge-os/veilbridge/issues/36)). Verified on
  the router through the screen itself: a forwarded port answered from outside
  and closed again when removed; a rule blocking the panel was put in front of
  the rule allowing it and came back by itself when nobody confirmed.
- Settings search finds sections by the words people use for them: "port
  forwarding", "проброс портов", "NAT" find the firewall, "DHCP" the local
  network, "PPPoE" the internet connection — and says which word matched
  ([#32](https://github.com/veilbridge-os/veilbridge/issues/32)).

### Changed
- The panel uses the words router owners already know: **DNS servers**
  instead of "Resolvers", and **MAC address** instead of "Hardware address",
  in both languages and in the list of staged changes
  ([#26](https://github.com/veilbridge-os/veilbridge/issues/26)).

### Fixed
- A change to one of several entries — a rule, a port forward, a reserved
  address — now says **which** entry it is about. Switching off the rule that
  keeps the panel reachable was listed as just "Rule is on: yes → no", next to
  other changes, with nothing to say which rule.
- A port forward being removed was listed, after reloading the page, with the
  word `redirect` instead of the forward itself ("tcp 8443 → 192.168.1.50:443"),
  exactly when the operator was about to confirm it. Present in
  `v0.2.0-alpha1`.
- The address kept for a device can be changed again: pinning a device the
  panel had already pinned to another address was refused with "not a valid
  section name", because the panel creates entries without a name and then
  refused to address them by position. Present in `v0.2.0-alpha1`.
- A warning about losing access now says how to get back: on the local
  network screen — reconnect, open the new address (shown) and confirm in
  time; on the internet screen — a cable into a local network port and the
  panel's address there, which does not depend on the uplink
  ([#31](https://github.com/veilbridge-os/veilbridge/issues/31)).
- Lease time in the list of changes reads "12 hours → 2 hours" instead of the
  device's spelling "12h → 2h"; a lease set in days or weeks (`1d`, `1w`, as
  LuCI allows) is no longer read as zero
  ([#30](https://github.com/veilbridge-os/veilbridge/issues/30)).
- The warning read before applying ("settings come back by themselves in N
  seconds") now quotes the device's actual confirmation window instead of a
  number written into the interface, and the countdown bar measures from the
  real window even when the page is opened halfway through it
  ([#29](https://github.com/veilbridge-os/veilbridge/issues/29)).
- When the device refuses a value, the refusal now always lands next to the
  field it is about, on the internet and local network screens and in the
  pin dialog. The device names the field (`errors[].location` in the API)
  instead of the panel guessing it from the wording
  ([#28](https://github.com/veilbridge-os/veilbridge/issues/28)); on the local
  network screen the message is also in the interface language now, followed
  by the device's own words.
- A change to the **local network** address was listed for confirmation as
  "Connection type / Address on the internet side / Network mask" — the words
  of the uplink. The device now sends a stable key with every row
  (`labelKey`), the panel translates exactly that key, and the build fails if
  any key the device can send has no translation
  ([#27](https://github.com/veilbridge-os/veilbridge/issues/27)).
- On a 360 px phone the list of staged changes no longer runs 3 px off the
  screen in Russian, and the "pin an address by hand" dialog is no longer cut
  off at the right edge.

### Known issues
- Static routes, emergency access, Wi-Fi and clients are not managed yet.
- The panel speaks plain HTTP.
- Firewall rules with conditions the panel does not show (source address,
  schedule, rate limit) can be switched on and off or removed, not edited.
- Switching an existing rule back on is not checked against the rules above
  it; a rule an earlier one already decides stays inert.

## [0.2.0-alpha1] — 2026-09-25 — pre-release

The first public build of the `v0.2` line. It is a **pre-release**: the version
is not complete (firewall, static routes and emergency access are still to
come), and it is never offered as the latest release.

### Added
- Safe apply: network changes are staged, shown as a before/after diff in the
  panel's own words, and applied through a transaction that restores the
  previous settings by itself if the operator's browser does not confirm in
  time — also when the daemon dies inside the confirmation window.
- Internet (uplink) screen: DHCP, static address or PPPoE, DNS servers.
- Local network screen: router address, address handout and lease time, the
  clients holding an address, and reserving an address for a device from the
  client table.
- Capabilities: the panel hides what the hardware cannot do and says why.
- Live updates over one event stream; device vitals (model, firmware, load,
  memory, writable space) with a short history kept in RAM only.
- The tunnel engine is chosen by probing the kernel: kernel TUN where
  available, a userspace fallback otherwise; the choice is shown in the API.
- Rebuilt panel: new shell and dashboard, dark theme, layouts from 360 px.
- `install.sh`: `VB_VERSION` installs a specific release,
  `VB_ALLOW_DOWNGRADE=1` goes back on purpose (without it the installer never
  moves a router to an older version, on opkg or apk), and the result is
  verified against the release checksum instead of trusting the package
  manager.

### Changed
- The panel no longer polls; the dashboard reads the event stream.
- The daemon refuses to start outside OpenWrt; `-demo` runs the panel anywhere.

### Known issues
- Firewall, port forwarding, static routes, Wi-Fi and clients are not managed
  yet — keep LuCI for those.
- The panel speaks plain HTTP.
- Some labels still use code words ("Resolvers") —
  [#26](https://github.com/veilbridge-os/veilbridge/issues/26).
- The binary is ~2.4 MB larger than v0.1 because the userspace engine is now
  reachable as a fallback on every device.

## [0.1.2] — 2026-09-25

A packaging fix on the `v0.1` line. The daemon and the panel are unchanged.

### Fixed
- **OpenWrt 25.12 and later can install VeilBridge.** v0.1.1 shipped only
  `.ipk`, and 25.12 replaced opkg with apk, so the one-line installer failed with
  "download failed". Every release now ships `.apk` next to `.ipk`, and
  `install.sh` picks the one the router can use.
- A package for the wrong CPU no longer kills a working installation. Under
  opkg it is refused before anything is unpacked; under apk, which cannot refuse,
  the working binary is saved and put back.

## [0.1.1] — 2026-09-19

### Added
- OpenWrt package (`.ipk`) with a procd service that starts on boot, an
  owner-only `/etc/veilbridge`, and a one-command installer that verifies the
  checksum.

### Changed
- The shared manager wiring now lives in the OpenWrt adapter, the only
  supported platform.

## [0.1.0] — 2026-09-19

First public release.

### Added
- Import AmneziaWG configuration files, obfuscation parameters included.
- Exit node list with live handshake status; switch the active exit in one click.
- Dashboard: WAN address, tunnel egress address, tunnel status, CPU, memory, uptime.
- Routing rules: which domains and subnets go through the tunnel or direct (nftables).
- Path checks that compare egress addresses instead of trusting `200 OK`.
- 13 interface languages (English and Russian complete).
- `-demo` mode, `-version`, static binaries for amd64 and arm64 with the UI embedded.

[Unreleased]: https://github.com/veilbridge-os/veilbridge/compare/v0.2.0-alpha2...HEAD
[0.2.0-alpha2]: https://github.com/veilbridge-os/veilbridge/compare/v0.2.0-alpha1...v0.2.0-alpha2
[0.2.0-alpha1]: https://github.com/veilbridge-os/veilbridge/compare/v0.1.2...v0.2.0-alpha1
[0.1.2]: https://github.com/veilbridge-os/veilbridge/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/veilbridge-os/veilbridge/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/veilbridge-os/veilbridge/releases/tag/v0.1.0
