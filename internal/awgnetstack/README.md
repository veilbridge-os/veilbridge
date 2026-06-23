# internal/awgnetstack

Thin vendored fork of `amneziawg-go/tun/netstack` with a one-line gVisor-compat
patch: `pkt.IsNil()` → `pkt == nil`.

**Why:** Xray-core pins gVisor 2026-01-22, which removed `PacketBuffer.IsNil()`.
amneziawg-go's upstream netstack still calls it, so a combined build fails to
compile without this patch. Keep this as a thin tracked fork; drop it once
amneziawg-go bumps its own gVisor pin.

Verified empirically: combined binary (amneziawg-go + Xray-core) compiles and
runs in-process with this patch (~29MB stripped).
