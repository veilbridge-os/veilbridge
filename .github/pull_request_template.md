# What and why

<!-- One paragraph: what changes, and which problem it solves. Link the issue. -->

## How it was verified

<!--
"It compiles" is not verification. State what you ran and what it printed.
Anything that touches the router (uci, nftables, interfaces, the tunnel) needs
a run on a real OpenWrt target or an OpenWrt VM — not just unit tests.
-->

- [ ] `go vet ./...` and `go test ./...` pass
- [ ] `cd web && npm run lint && npm run build` pass (UI changes)
- [ ] OpenAPI snapshot regenerated if handlers changed
      (`go run ./cmd/veilbridged -dump-openapi > api/openapi.yaml`)
- [ ] Verified on an OpenWrt target (say which device or VM, and the release)
- [ ] Does not touch the router — unit tests and `-demo` are enough here
- [ ] Hardware-dependent behaviour degrades to a reported capability, not to a
      broken button or a silent failure

Evidence (commands, output, screenshots):

```text

```

## Architecture checklist

- [ ] OS specifics stay in `internal/adapters/*`; `api` and `core` stay
      platform-agnostic
- [ ] No real infrastructure in the repo — addresses in docs, tests and
      screenshots come from the RFC 5737 documentation ranges
- [ ] No secrets, keys or password hashes added, even in fixtures
- [ ] Commit messages are single-line subjects (the repo hook rejects
      multi-line `-m`)
