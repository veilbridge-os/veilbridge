# VeilBridge

> A modern, cross-platform router & VPN-gateway control panel.
> One clean UI instead of SSH and hand-edited configs.

VeilBridge is an open-source control plane for VPN gateways. It runs as a single
lightweight Go binary on both **OpenWrt** routers and **Ubuntu/Debian** servers,
and exposes a clean web UI (Vue + Element Plus) on top of a platform-agnostic API.

The UI never talks to `uci`, `systemd`, or `nftables` directly — all OS specifics
live behind **adapters**, so the same binary works across platforms.

## Status

🚧 **Early development.** Targeting an `v0.1` MVP. Not production-ready yet.

## Features (planned MVP — v0.1)

- **Import VPN configs** — AmneziaWG config files and subscriptions
- **Exit node list** — see your gateways with handshake status and throughput
- **One-click exit switch** — change the active exit node from the dashboard
- **Dashboard** — WAN IP, current exit geo, tunnel status, CPU / RAM / uptime
- **Routing control** — manage which domains/subnets go through the tunnel vs direct
- **Path-aware checks** — verify traffic actually flows through the tunnel (not just `200 OK`)

## Roadmap

| Version | Highlights |
|---------|-----------|
| `v0.1`  | AmneziaWG engine, own routing, dashboard, OpenWrt + Ubuntu adapters |
| `v0.2`  | FakeIP, health-check failover between nodes, dynamic rule lists |
| `v0.3`  | VLESS + REALITY via Xray-core, automatic node selection (urltest) |
| `v0.4+` | Wi-Fi & device management, plugin/component system, remote access |

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

## Building

```bash
# (coming soon — project is scaffolding stage)
go build ./cmd/veilbridged
```

## Contributing

Contributions are welcome once the MVP scaffolding lands. See `docs/` for design notes.

## License

[MIT](./LICENSE)
