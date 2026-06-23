module github.com/veilbridge-os/veilbridge

go 1.26.3

toolchain go1.26.4

// The gVisor pin is the fragile joint — gate any bump behind a CI build.
// VERIFIED (Phase 3): amneziawg-go's own gVisor pin (~2025-06) has a package-name
// inconsistency (bridge_test vs stack_test) that breaks `go build`; the newer
// gVisor `go` branch (~2026-06) fixes it but removed PacketBuffer.IsNil(). So we
// force the newer gVisor and vendor amneziawg's netstack with a one-line patch
// (internal/awgnetstack). With Xray (v0.3) MVS picks this same newer pin.
// See docs/embedding-notes.md.
//
//	github.com/amnezia-vpn/amneziawg-go v1.0.4
//	github.com/xtls/xray-core           v1.260327.0 (v0.3 — not yet imported)
//	gvisor.dev/gvisor                   go-branch ~2026-06 (forced; vendored patch)
//
// API layer — code-first OpenAPI 3.1 (DESIGN D-8). Swagger UI / spec endpoints are
// dev-only (DESIGN D-9). See internal/api.
//
//	github.com/danielgtaylor/huma/v2    v2.x

require (
	github.com/amnezia-vpn/amneziawg-go v1.0.4
	github.com/danielgtaylor/huma/v2 v2.38.0
	github.com/golang-jwt/jwt/v5 v5.3.1
	golang.org/x/crypto v0.53.0
	golang.org/x/net v0.55.0
	gvisor.dev/gvisor v0.0.0-20260622202500-b859e3a10a38
)

require (
	github.com/google/btree v1.1.3 // indirect
	github.com/tevino/abool v1.2.0 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	golang.org/x/exp v0.0.0-20250711185948-6ae5c78190dc // indirect
	golang.org/x/sys v0.46.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	golang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2 // indirect
)
