# What and why

<!-- One paragraph: what changes, and which problem it solves. Link the issue. -->

## How it was verified

<!--
"It compiles" is not verification. State what you ran and what it printed.
Platform-touching changes need a run on the platform they touch — or an
explicit note that the capability is reported as unavailable there.
-->

- [ ] `go vet ./...` and `go test ./...` pass
- [ ] `cd web && npm run lint && npm run build` pass (UI changes)
- [ ] OpenAPI snapshot regenerated if handlers changed
      (`go run ./cmd/veilbridged -dump-openapi > api/openapi.yaml`)
- [ ] Verified on OpenWrt
- [ ] Verified on Ubuntu/Debian
- [ ] Not applicable to a platform — and the code says so via capabilities,
      not via a silent failure

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
