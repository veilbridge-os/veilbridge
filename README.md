# VeilBridge

> A modern control panel for **OpenWrt** routers, built around VPN and
> selective routing. One clean UI instead of SSH and hand-edited configs.

VeilBridge is an open-source control plane for OpenWrt routers. It runs as a
single lightweight Go binary on the router itself and exposes a clean web UI
(Vue + Element Plus) on top of a platform-agnostic API.

The UI never talks to `uci`, `ubus`, or `nftables` directly — all OS specifics
live behind **adapters**. The goal is the feature level of a commercial router
OS, on any hardware that runs OpenWrt, plus honest domain-based tunnel routing
and proof of where your traffic actually leaves.

![VeilBridge dashboard](docs/img/dashboard.png)

## Status

🧪 **v0.1 MVP — feature-complete, in testing.** The core value path works end to
end and is verified on a real OpenWrt target: the binary brings a tunnel up via
the kernel engine, `awg0` appears, and the dashboard confirms traffic egresses
through it — checked by comparing egress IPs, not by trusting a `200 OK`.

This is an early release: VeilBridge manages VPN, selective routing and the
dashboard today. It does **not** yet manage WAN/LAN, DHCP, firewall zones,
Wi-Fi or clients — keep LuCI around for those. See [Install](#install) for the
release binaries and the [roadmap](#roadmap) for what's next.

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
| --- | --- | --- |
| `v0.1` | AmneziaWG engine, own routing, dashboard, OpenWrt adapter | ✅ feature-complete |
| `v0.2` | Platform layer (uci/ubus) with safe apply + rollback; router network: WAN/LAN, DHCP, firewall | planned |
| `v0.3` | Devices & Wi-Fi; exit-node policies, health-check failover | planned |
| `v0.4` | FakeIP and domain routing; DNS with per-device profiles and filters | planned |
| `v0.5+` | App platform and market (VLESS/Xray, auto-bypass as apps), VPN servers, QoS, remote access | planned |

## Architecture

```text
   Web UI (Vue + Element Plus)
            │  REST / JSON
            ▼
   veilbridged  (single Go binary)
   ├─ Router Core API        ← the only contract the UI sees
   ├─ embedded UI (go:embed)
   ├─ managers (interfaces)  ← VPN / Routing / Network / System
   └─ VPN engines            ← AmneziaWG (Xray-core later)
            │
            ▼
      OpenWrt adapter     ← uci / ubus / nftables / procd live here only
```

The adapter boundary is not decoration: it is what keeps `uci` out of the API
and the UI, and it is where a second platform would plug in if the project ever
needs one.

## Tech stack

- **Backend / agent:** Go (single static binary, cross-compiled for x86 / ARM)
- **Frontend:** Vue 3 + Element Plus (built to static assets, embedded into the binary)
- **VPN:** [amneziawg-go](https://github.com/amnezia-vpn/amneziawg-go) (MIT); [Xray-core](https://github.com/XTLS/Xray-core) (MPL-2.0) from v0.5

## Install

Grab a static binary from the
[latest release](https://github.com/veilbridge-os/veilbridge/releases/latest) —
no runtime, no dependencies, the web UI is inside the binary:

Run these **on the router** (`ssh root@192.168.1.1`). Check the architecture
with `uname -m`: `x86_64` → `amd64`, `aarch64` → `arm64`.

```bash
ARCH=arm64
BASE=https://github.com/veilbridge-os/veilbridge/releases/latest/download

# Keep the published file name — the checksums are listed under it
curl -fL -O "$BASE/veilbridged-linux-$ARCH"
curl -fL -O "$BASE/SHA256SUMS"
sha256sum --check --ignore-missing SHA256SUMS   # must print: OK

mv "veilbridged-linux-$ARCH" veilbridged
chmod +x veilbridged
./veilbridged -version
```

On OpenWrt use `wget` and busybox `sha256sum` instead (no `--ignore-missing`):
`grep veilbridged-linux-$ARCH SHA256SUMS > one.sum && sha256sum -c one.sum`.

Then continue with [Running](#running). An opkg package, a signed feed and
ready-made firmware images are on the roadmap; until then the binary is the
supported path, and it does not install a service — it runs in the foreground.

**Requirements on the target:** OpenWrt with `kmod-tun` (for `/dev/net/tun`) and
`curl`; roughly 25 MiB of RAM for the daemon and ~13 MB of storage for the
binary, so an 8/64 MB device will not fit it.

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

The daemon refuses to start on a host that is not OpenWrt (it looks for
`/etc/openwrt_release`) — it manages the router's firewall and routing, and
guessing about a host managed by something else is how you lock yourself out.
Use `-demo` to run the panel anywhere.

## Running

```bash
# First run: set the admin password (stored bcrypt-hashed, never in plaintext)
./veilbridged -set-password 'choose-a-password'

# Start the daemon — serves the API and (with -tags ui) the dashboard on one port
sudo ./veilbridged -listen 0.0.0.0:8080
```

Root is needed to bring the tunnel up (creating the `awg0` interface and writing
nftables rules). On the router you are already root, so drop the `sudo`. Then
open `http://<router-ip>:8080/` and sign in.

### Trying it without a router

`-demo` serves sample data from an in-memory adapter — no root, no tunnels and
nothing touched on the host. It is also the only way to run the panel on a
non-OpenWrt machine (your laptop), which makes it the normal mode for UI work:

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
so adding a VPN engine or a platform means implementing an interface, not touching
the core. See [`CONTRIBUTING.md`](./CONTRIBUTING.md) and the per-package `README.md`
files under `internal/`.

## License

[MIT](./LICENSE)
