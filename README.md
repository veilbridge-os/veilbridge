# VeilBridge

> A modern, cross-platform router & VPN-gateway control panel.
> One clean UI instead of SSH and hand-edited configs.

VeilBridge is an open-source control plane for VPN gateways. It runs as a single
lightweight Go binary on both **OpenWrt** routers and **Ubuntu/Debian** servers,
and exposes a clean web UI (Vue + Element Plus) on top of a platform-agnostic API.

The UI never talks to `uci`, `systemd`, or `nftables` directly — all OS specifics
live behind **adapters**, so the same binary works across platforms.

![VeilBridge dashboard](docs/img/dashboard.png)

## Status

🧪 **v0.1 MVP — feature-complete, in testing.** The core value path works end to
end and has been verified on real hardware on **both** platforms: the *same*
binary brings a tunnel up via the userspace engine on Ubuntu and via the kernel
engine on OpenWrt, and the dashboard confirms traffic egresses through it.
See [Install](#install) for the release binaries and the [roadmap](#roadmap)
for what's next.

## Features (v0.1)

- **Import VPN configs** — AmneziaWG config files (obfuscation params included)
- **Exit node list** — your gateways with live handshake status
- **One-click exit switch** — change the active exit node from the UI
- **Dashboard** — WAN IP, tunnel egress IP, tunnel status, CPU / RAM / uptime
- **Routing control** — choose which domains/subnets go through the tunnel vs direct (nftables)
- **Path-aware checks** — verify traffic *actually* egresses through the tunnel, by comparing the egress IP, not by trusting `200 OK`
- **13 languages** — UI localized (full en/ru, the rest fall back to English)

## Roadmap

| Version | Highlights | Status |
|---------|-----------|--------|
| `v0.1`  | AmneziaWG engine, own routing, dashboard, OpenWrt + Ubuntu adapters | ✅ feature-complete |
| `v0.2`  | FakeIP, health-check failover between nodes, device policies, dynamic rule lists | planned |
| `v0.3`  | VLESS + REALITY via Xray-core, automatic node selection (urltest) | planned |
| `v0.4+` | Wi-Fi & device management, plugin/component system, remote access | planned |

## Architecture

```
   Web UI (Vue + Element Plus)
            │  REST / JSON
            ▼
   veilbridged  (single Go binary)
   ├─ Router Core API        ← the only contract the UI sees
   ├─ embedded UI (go:embed)
   ├─ managers (interfaces)  ← VPN / Routing / Network / System
   └─ VPN engines            ← AmneziaWG (Xray-core in v0.3)
            │
     ┌──────┴──────┐
     ▼             ▼
  OpenWrt        Ubuntu
  adapter        adapter
```

## Tech stack

- **Backend / agent:** Go (single static binary, cross-compiled for x86 / ARM)
- **Frontend:** Vue 3 + Element Plus (built to static assets, embedded into the binary)
- **VPN:** [amneziawg-go](https://github.com/amnezia-vpn/amneziawg-go) (MIT); [Xray-core](https://github.com/XTLS/Xray-core) (MPL-2.0) from v0.3

## Install

Grab a static binary from the
[latest release](https://github.com/veilbridge-os/veilbridge/releases/latest) —
no runtime, no dependencies, the web UI is inside the binary:

```bash
# Pick your architecture: amd64 (x86-64 server/VM) or arm64 (most routers)
ARCH=amd64
BASE=https://github.com/veilbridge-os/veilbridge/releases/latest/download

curl -fL -o veilbridged "$BASE/veilbridged-linux-$ARCH"
curl -fL -o SHA256SUMS "$BASE/SHA256SUMS"
sha256sum --check --ignore-missing SHA256SUMS

chmod +x veilbridged
./veilbridged -version
```

Then continue with [Running](#running). Packages (`.deb`, opkg) and firmware
images are on the roadmap; until then the binary is the supported path.

## Building

Requires **Go 1.26+** (the toolchain directive in `go.mod` pulls the exact
version) and **Node 22+** for the UI. The binary is pure Go (`CGO_ENABLED=0`),
so it cross-compiles to any target without a C toolchain.

```bash
# API-only daemon (no embedded UI)
go build -o veilbridged ./cmd/veilbridged

# With the embedded web UI — build the frontend first, then tag the Go build:
( cd web && npm ci && npm run build )
go build -tags ui -o veilbridged ./cmd/veilbridged

# Cross-compile a static binary for a router (linux/arm64):
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -tags ui -o veilbridged ./cmd/veilbridged
```

## Running

```bash
# First run: set the admin password (stored bcrypt-hashed, never in plaintext)
./veilbridged -set-password 'choose-a-password'

# Start the daemon — serves the API and (with -tags ui) the dashboard on one port
sudo ./veilbridged -listen 0.0.0.0:8080
```

`sudo`/root is needed to bring tunnels up (the userspace engine needs
`/dev/net/tun`; the kernel engine needs to create the `awg0` interface). Then
open `http://<host>:8080/` and sign in.

### Trying it without hardware

`-demo` serves sample data from an in-memory adapter — no root, no tunnels and
nothing touched on the host. Useful for a first look and for UI work:

```bash
./veilbridged -config /tmp/demo.json -set-password 'demo-password'
./veilbridged -demo -config /tmp/demo.json -listen 127.0.0.1:8099
```

The screenshots above are captured from exactly this mode by
[`scripts/screenshots.mjs`](./scripts/screenshots.mjs), so every address in them
is from the documentation ranges reserved by RFC 5737.

> **Security note:** the panel speaks plain HTTP today — run it on a trusted LAN
> or behind a TLS-terminating reverse proxy. Built-in TLS is on the roadmap.

## Contributing

Contributions are welcome. The architecture is hexagonal — `api → core ← adapters` —
so adding a platform or a VPN engine means implementing an interface, not touching
the core. See [`CONTRIBUTING.md`](./CONTRIBUTING.md) and the per-package `README.md`
files under `internal/`.

## License

[MIT](./LICENSE)
