# Changelog

What changed for a person running VeilBridge on a router. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and versions follow
[semantic versioning](https://semver.org/); how releases are planned and cut is
in [`RELEASING.md`](./RELEASING.md). Each release also has written notes in
[`docs/release-notes/`](./docs/release-notes/).

## [Unreleased]

### Changed
- The panel uses the words router owners already know: **DNS servers**
  instead of "Resolvers", and **MAC address** instead of "Hardware address",
  in both languages and in the list of staged changes
  ([#26](https://github.com/veilbridge-os/veilbridge/issues/26)).

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
