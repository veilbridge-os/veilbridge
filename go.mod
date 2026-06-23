module github.com/veilbridge-os/veilbridge

go 1.26

toolchain go1.26.2

// Pinned engine versions (the gVisor pin is the fragile joint between the two —
// gate any bump behind a CI build). See docs/embedding-notes.md.
//
//	github.com/amnezia-vpn/amneziawg-go v1.0.4
//	github.com/xtls/xray-core           v1.260327.0
//	gvisor.dev/gvisor                   <2026-01-22> (forced by xray-core via MVS)
