# Contributing to VeilBridge

Thanks for your interest! VeilBridge is a single Go binary that controls an
OpenWrt router through one platform-agnostic API and an embedded Vue UI. This
guide covers the layout, the conventions, and how to run the checks CI runs.

## Architecture in one minute

The codebase is **hexagonal**: the API depends only on core interfaces, and the
OS plugs in behind an adapter. Nothing in `api` or `core` knows about `uci`,
`nftables`, or `/dev/net/tun`. OpenWrt is the only supported platform, but the
boundary is kept honest anyway — it is what stops `uci` from leaking into the
API and the UI.

```text
internal/
  api/        Router Core API (Huma, code-first) — the only contract the UI sees
  core/       domain types + manager interfaces (VPN/Routing/System/...) + mocks
              apply.go: the transaction (snapshot → commit → confirm or revert)
  config/     persisted state (atomic JSON store, bcrypt password, node secrets)
  metrics/    in-RAM vitals history; nothing here ever touches flash
  architecture/ a guard test: OS specifics may not leak past the adapter
  routing/    nftables rule generator (engine-agnostic)
  vpn/        Engine abstraction; amneziawg/ has the userspace + kernel engines
  adapters/
    detect.go platform detection (non-OpenWrt hosts are refused)
    openwrt/  the adapter: engines, nft routing, ubus reads, uci staging,
              capability probes, and the uci-backed applier
  awgnetstack/ vendored, patched amneziawg-go netstack (one-line gVisor fix)
cmd/
  veilbridged/ the daemon (loads config → builds adapter → serves API + UI)
  p4smoke/     standalone Phase-4 tunnel smoke test (not part of the product)
web/           Vue 3 + Element Plus; built to dist/ and embedded via go:embed
```

### Two rules that are easy to break by accident

**Reading is safe, writing is not.** A getter may talk to the device
(`NetworkManager` reads netifd over ubus). Changing configuration goes through
`core.NetworkWriter`, which only *stages* — `uci set` writes a draft and
nothing else. The single moment anything becomes live is the apply
transaction's commit, and it runs under a watchdog that restores the snapshot
if nobody confirms. Folding "stage" and "commit" into one call would quietly
undo the safety model that was proven by cutting a real router's own
management link; there is a test asserting staging never commits.

**Anything on a timer must not leave the device.** `SystemManager.Info()`
resolves the public address by asking a third party, which is fine once for a
human looking at a dashboard and is a permanent outbound stream when something
polls it. Pollers use `core.Vitals` (local reads only). This is not
hypothetical: it shipped for an hour and showed up in the router's conntrack as
a connection every three seconds.

**Adding a VPN engine** = implement `vpn.Engine`; the adapter picks which engine
to use. **Adding a platform** = implement `core.Adapter` (and the managers it
returns), then wire it into `adapters.New` by detection. You should not need to
touch `api` or `core` for either.

The userspace and kernel engines share UAPI rendering and stats parsing — only
the TUN differs (gVisor netstack vs a kernel `awg0`). The kernel engine is the
product path: it creates a real `awg0` interface that nftables can forward the
whole LAN through. The userspace engine needs no `kmod-tun` and is the fallback
for devices that lack it. The choice is made by probing `/dev/net/tun` at
start up — the same probe that fills the `kernel-tun` capability, so the
panel's explanation and the daemon's actual choice cannot drift apart. The
engine in use is reported as `tunnelEngine` in `GET /system`.

## Development

Prerequisites: **Go 1.26+**, **Node 22+**.

```bash
# Backend
go vet ./...
go test ./...
go build ./cmd/veilbridged

# Frontend
cd web
npm ci
npm run lint      # Biome
npm run build     # runs vue-tsc (type-check) + bundles into dist/

# Full binary with embedded UI
( cd web && npm run build ) && go build -tags ui ./cmd/veilbridged
```

The on-hardware end-to-end script lives in [`scripts/`](./scripts/)
(`p8-e2e.sh`). It needs an OpenWrt stand and real AmneziaWG nodes, so it is not
part of CI. Without a router, `-demo` runs the panel anywhere.

## Conventions

- **The API is code-first.** `api/openapi.yaml` is **generated**, not hand-edited:
  `go run ./cmd/veilbridged -dump-openapi > api/openapi.yaml`. CI fails if it drifts.
- **Frontend types come from the spec.** Run `npm run gen-api` after API changes so
  `web/src/api/schema.ts` matches the contract.
- **i18n is type-safe.** UI strings live in `web/src/i18n/messages/*`; keys are
  checked against the English schema at compile time. Add keys to `en` first.
- **Secrets never reach the API.** Node private keys / PSKs live in `config` only;
  the API exposes the public `core.Node`. Keep it that way.
- **Liveness is by egress, not by status code.** Path checks compare the tunnel
  egress IP against the direct WAN — a `200 OK` does not prove the tunnel.
- Run `gofmt` and Biome before committing; both are enforced in CI.

## Commits & PRs

- Keep commits focused; a clear `type(scope): summary` subject is appreciated.
- Make sure `go vet`, `go test`, `npm run lint`, and `npm run build` pass — these
  are exactly what CI runs (on `ubuntu-24.04` runners).
- Describe *what* changed and *why*; note anything you verified by hand.

## License

By contributing you agree your contributions are licensed under the
[MIT License](./LICENSE).
