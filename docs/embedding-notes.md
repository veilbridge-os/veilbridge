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

## The one required patch (gVisor version conflict)

Both libraries import `gvisor.dev/gvisor` but pin different versions. Go's MVS picks
Xray's newer pin (2026-01-22), which removed `PacketBuffer.IsNil()`. amneziawg-go's
upstream `tun/netstack` still calls it, so a naive combined build fails to compile.

**Fix:** one line — `pkt.IsNil()` → `pkt == nil` — in a vendored copy of the
netstack package. See [`internal/awgnetstack/`](../internal/awgnetstack/). The
conflict touches **only** that file; amnezia's core/device/kernel-TUN packages
build clean.

## Build facts

- **Go toolchain:** ≥ 1.26 required (Xray declares `go 1.26`).
- **`CGO_ENABLED=0`** → fully static binary, good for OpenWrt/musl cross-builds.
- **Binary size (stripped `-s -w`):** Xray dominates (~27MB); AmneziaWG adds ~2MB;
  combined ≈ **29MB**. Fine on router flash.
- **Pin exact versions** in go.mod; gate dependency bumps behind a CI build, because
  the gVisor pin is the fragile joint between the two engines.

## Not yet verified (follow-up before committing architecture)

- **End-to-end packet flow** AmneziaWG → Xray → internet (no live REALITY server in
  the test sandbox). Smoke test: wire `awgNet.DialContext` into Xray's outbound
  dialer against a real REALITY endpoint.
- **Runtime RAM footprint on 256MB hardware** — measure on the actual router target.
  gVisor + Xray working-set is the real constraint, not the 29MB binary size.
