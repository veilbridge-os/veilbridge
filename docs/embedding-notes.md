# Embedding the VPN engines in-process

VeilBridge runs its VPN engines **inside the agent binary** — no sidecar process,
no `exec`. This was verified empirically (a combined binary was built and run).

## AmneziaWG (amneziawg-go, MIT)

- Imported as a library: `device.NewDevice(tun, bind, logger)`.
- Configured in-process via `(*Device).IpcSet(uapiConf)`.
- Amnezia obfuscation params are first-class UAPI keys: `jc`, `jmin`, `jmax`,
  `s1`–`s4`, `h1`–`h4`. They parse and apply in-process.
- **Userspace path (no root TUN):** `netstack.CreateNetTUN(...)` returns a `*Net`
  with `DialContext` / `Dial` / `ListenTCP` / `DialUDP`. The whole tunnel lives in
  the process. This is the path VeilBridge uses.

## Xray-core (VLESS + REALITY, MPL-2.0)

- Imported as a library: `serial.LoadJSONConfig(reader)` → `core.New(config)` →
  `(*Instance).Start()`.
- **Must** blank-import `_ "github.com/xtls/xray-core/main/distro/all"` to register
  all protocols/transports — without it VLESS/REALITY are not registered.
- Reference implementation: XTLS's own [libXray](https://github.com/XTLS/libxray).
- License: MPL-2.0 is file-level copyleft. Importing (no modification) does **not**
  affect our MIT code. Only modified Xray `.go` files must stay MPL and be published.

## The gVisor pin conflict — VERIFIED in Phase 3 (and the original note corrected)

This was empirically pinned down while building Phase 3. The real problem is a
**two-horned dilemma**, and it bites at v0.1 already (no Xray needed):

- **amneziawg-go's own gVisor pin** (`~2025-06-06`): `PacketBuffer.IsNil()` still
  exists ✅, BUT that gVisor snapshot's `pkg/tcpip/stack` dir mixes `bridge_test`
  and `stack_test` package names — Go's loader refuses it, so even a plain
  `go build` of `tun/netstack` (via the gonet adapter) fails. ❌
- **The newer gVisor `go` branch** (`~2026-06`): the package-name inconsistency is
  gone ✅, BUT `PacketBuffer.IsNil()` was removed, so amneziawg's upstream
  `tun/netstack/tun.go:158` (`pkt.IsNil()`) no longer compiles. ❌

> Correction: the earlier draft (from a research agent) claimed amnezia's netstack
> "builds clean" and blamed only Xray's pin. Both points were wrong — the break is
> real at v0.1, and it's a packaging quirk + a removed method, not just MVS.

**Fix (verified to compile):** force the newer gVisor pin AND vendor amneziawg's
`tun/netstack` as [`internal/awgnetstack/`](../internal/awgnetstack/) with one line
changed — `pkt.IsNil()` → `pkt == nil`. The vendored file keeps the upstream MIT
header and documents the single diff. amnezia's `conn`/`device` packages build
clean against the newer gVisor; only `tun/netstack` needed the patch.

Re-derive the patch on any amneziawg-go bump: diff `internal/awgnetstack/tun.go`
against the upstream `tun/netstack/tun.go`; only that one line should differ.

## Build facts

- **Go toolchain:** ≥ 1.26 required (Xray declares `go 1.26`).
- **`CGO_ENABLED=0`** → fully static binary, good for OpenWrt/musl cross-builds.
- **Binary size (stripped `-s -w`):** Xray dominates (~27MB); AmneziaWG adds ~2MB;
  combined ≈ **29MB**. Fine on router flash.
- **Pin exact versions** in go.mod; gate dependency bumps behind a CI build, because
  the gVisor pin is the fragile joint between the two engines.

## Verified vs not (status after Phase 3)

**Verified (Phase 3):** the combined build compiles — `netstackEngine`
(`internal/vpn/amneziawg`) brings up a userspace AmneziaWG tunnel via the vendored
patched netstack, builds `CGO_ENABLED=0`, and the UAPI rendering / stats parsing
are unit-tested. The gVisor dilemma above is resolved and reproducible.

**Live packet flow — VERIFIED, Phase 4 gate PASSED.** `cmd/p4smoke` cross-compiled
to linux/amd64 (static, 9.1MB — AmneziaWG only, no Xray) and run on the RU-VPS test
box (198.51.100.5, Ubuntu, no Go installed) tunnelling into the FI node
(203.0.113.20:51820):
- direct egress (no tunnel): `198.51.100.5` (RU)
- `netstackEngine.Up` → handshake in ~1s → egress THROUGH `Dialer`: `203.0.113.20` (FI)
- → traffic genuinely takes the tunnel; the "handshake ≠ traffic" trap is cleared.

**Runtime RAM — VERIFIED, far below worry.** `/usr/bin/time -v` reported **Maximum
resident set size ≈ 11.7 MB RSS** for the whole userspace tunnel (gVisor netstack +
amneziawg-go device). The earlier fear that gVisor's working-set would dominate on
256MB hardware does NOT hold for AmneziaWG-only. (Re-measure once Xray/REALITY is
added in v0.3 — that's the heavier engine.)

**Still not verified (later):**
- **AmneziaWG → Xray → internet** chaining (v0.3): wire `Dialer` into Xray's
  outbound dialer against a real REALITY endpoint. Re-measure RAM then.
