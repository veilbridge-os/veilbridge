# internal/awgnetstack

Thin vendored fork of `amneziawg-go/tun/netstack` (v1.0.4, MIT) with a one-line
gVisor-compat patch: `pkt.IsNil()` → `pkt == nil` (in `WriteNotify`).

**Why (verified in Phase 3):** amneziawg-go's own gVisor pin (~2025-06) breaks
`go build` (a `bridge_test`/`stack_test` package-name inconsistency in
`pkg/tcpip/stack`); the newer gVisor `go` branch fixes that but removed
`PacketBuffer.IsNil()`. We force the newer gVisor and apply the one-line fix here.
The upstream MIT header is preserved; only that single line differs.

Re-derive on any amneziawg-go bump: diff `tun.go` against the upstream
`tun/netstack/tun.go` — only the one line should differ. Drop this fork once
amneziawg-go itself targets a gVisor with the field removed. See
`docs/embedding-notes.md` and the architecture notes in CONTRIBUTING.md
