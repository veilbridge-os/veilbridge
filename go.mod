module github.com/veilbridge-os/veilbridge

go 1.26

toolchain go1.26.4

// Pinned engine versions (the gVisor pin is the fragile joint between the two —
// gate any bump behind a CI build). See docs/embedding-notes.md.
//
//	github.com/amnezia-vpn/amneziawg-go v1.0.4
//	github.com/xtls/xray-core           v1.260327.0
//	gvisor.dev/gvisor                   <2026-01-22> (forced by xray-core via MVS)
//
// API layer — code-first OpenAPI 3.1 (DESIGN D-8). Swagger UI / spec endpoints are
// dev-only (DESIGN D-9). See internal/api.
//
//	github.com/danielgtaylor/huma/v2    v2.x

require golang.org/x/crypto v0.53.0
