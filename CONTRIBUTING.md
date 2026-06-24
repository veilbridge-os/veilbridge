# Contributing to VeilBridge

Thanks for your interest! VeilBridge is a single Go binary that controls VPN
gateways on OpenWrt and Ubuntu/Debian through one platform-agnostic API and an
embedded Vue UI. This guide covers the layout, the conventions, and how to run
the checks CI runs.

## Architecture in one minute

The codebase is **hexagonal**: the API depends only on core interfaces, and each
OS plugs in behind an adapter. Nothing in `api` or `core` knows about `uci`,
`nftables`, or `/dev/net/tun`.

```
internal/
  api/        Router Core API (Huma, code-first) — the only contract the UI sees
  core/       domain types + manager interfaces (VPN/Routing/System/...) + mocks
  config/     persisted state (atomic JSON store, bcrypt password, node secrets)
  routing/    nftables rule generator (shared by both adapters)
  vpn/        Engine abstraction; amneziawg/ has the userspace + kernel engines
  adapters/
    ubuntu/   userspace (netstack) engine + /proc system info
    openwrt/  kernel engine (transparent nft forwarding) — reuses ubuntu wiring
  awgnetstack/ vendored, patched amneziawg-go netstack (one-line gVisor fix)
cmd/
  veilbridged/ the daemon (loads config → builds adapter → serves API + UI)
  p4smoke/     standalone Phase-4 tunnel smoke test (not part of the product)
web/           Vue 3 + Element Plus; built to dist/ and embedded via go:embed
```

**Adding a platform** = implement `core.Adapter` (and the managers it returns),
then wire it into `adapters.New` by detection. **Adding a VPN engine** = implement
`vpn.Engine`; the adapter picks which engine to use. You should not need to touch
`api` or `core` for either.

The userspace and kernel engines share UAPI rendering and stats parsing — only the
TUN differs (gVisor netstack vs a kernel `awg0`). That symmetry is the point: the
*same binary* runs on both platforms.

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

The on-hardware end-to-end scripts live in [`scripts/`](./scripts/) (`p5-e2e.sh`
for the Ubuntu adapter, `p8-e2e.sh` for OpenWrt). They need a test stand and real
AmneziaWG nodes, so they are not part of CI.

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
  are exactly what CI runs on `ubuntu-latest`.
- Describe *what* changed and *why*; note anything you verified by hand.

## License

By contributing you agree your contributions are licensed under the
[MIT License](./LICENSE).
