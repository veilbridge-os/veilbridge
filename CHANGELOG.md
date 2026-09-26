# Changelog

What changed for a person running VeilBridge on a router. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and versions follow
[semantic versioning](https://semver.org/); how releases are planned and cut is
in [`RELEASING.md`](./RELEASING.md). Each release also has written notes in
[`docs/release-notes/`](./docs/release-notes/).

## [Unreleased]

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
- A `dhcp-server` capability: a device with one network port and no Wi-Fi has
  no local network to hand addresses out on, says so with a reason, and the
  panel does not show a local network section there at all
  ([#34](https://github.com/veilbridge-os/veilbridge/issues/34)).

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

[Unreleased]: https://github.com/veilbridge-os/veilbridge/compare/v0.2.0-alpha1...HEAD
[0.2.0-alpha1]: https://github.com/veilbridge-os/veilbridge/compare/v0.1.2...v0.2.0-alpha1
[0.1.2]: https://github.com/veilbridge-os/veilbridge/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/veilbridge-os/veilbridge/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/veilbridge-os/veilbridge/releases/tag/v0.1.0
