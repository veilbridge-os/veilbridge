# packaging/

How VeilBridge is delivered to a router. One recipe is the source of truth for
the service, the permissions and the dependencies — not a wiki page and not a
hand-made file on someone's test stand.

- [`nfpm.yaml`](./nfpm.yaml) — the package recipe. One recipe, two formats:
  `.ipk` for `opkg` (OpenWrt ≤ 24.10) and `.apk` for `apk-tools 3`
  (OpenWrt ≥ 25.12). `VERSION` and `VB_ARCH` come from the release workflow.
- [`openwrt/etc/init.d/veilbridge`](./openwrt/etc/init.d/veilbridge) — procd
  service, `START=95`, respawn with back-off, restarts when the config changes.
- [`scripts/`](./scripts) — `preinst.sh.in` / `postinst` / `prerm` / `postrm`.

Build one locally (needs the binary in `dist/` first). `preinst` is generated,
not committed as-is: the target CPU is substituted into it, because nfpm expands
environment variables inside `contents` but not inside script files.

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -tags ui \
  -o dist/veilbridged-linux-arm64 ./cmd/veilbridged

sed 's/@VB_ARCH@/arm64/g' packaging/scripts/preinst.sh.in > dist/preinst.sh

VERSION=v0.0.0-dev VB_ARCH=arm64 \
  go run github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.47.0 \
  package -f packaging/nfpm.yaml -p ipk -t dist/veilbridge_arm64.ipk
```

## Two decisions worth knowing before editing

**`Architecture: all`, on purpose.** OpenWrt architecture strings are
per-subtarget (`aarch64_cortex-a53`, `aarch64_cortex-a72`, …). A package stamped
`aarch64` is refused by `opkg` on real routers even though the binary runs
there — `opkg print-architecture` only accepts the device's own subtarget plus
`all`/`noarch`. So the package is architecture-independent, one file per CPU
family, and the CPU is checked *for real* by us.

**The CPU guard lives in `preinst`, not in `postinst`** — measured on a Cudy
WR3000S v1 (aarch64, OpenWrt 24.10.8): with the check in `postinst`, installing
`veilbridge_amd64.ipk` printed a clear explanation *after* the x86 binary had
already replaced the working one, leaving `/usr/bin/veilbridged` unrunnable
(`ELF: not found`) and the panel down. opkg runs `preinst` before extracting any
data file and aborts the whole install when it fails (`libopkg/opkg_install.c`:
`preinst_configure()` → "Aborting installation"), so refusing there keeps the
working installation intact. `postinst` still executes the binary as a second
line of defence.

One opkg detail this depends on: it has **no unwind for `prerm`** —
`prerm_upgrade_old_pkg_unwind()` is a stub that returns 0, with a note that dpkg
would run `old-postinst abort-upgrade` there. The old package's `prerm` has
already stopped the service by the time our `preinst` refuses, so `preinst`
starts it again itself (marker `/tmp/veilbridge.was-running`, plus the autostart
flag as a fallback when the installed version is too old to write the marker).
Verified on the router: wrong-CPU install → `rc=255`, version unchanged, panel
still answering.

**Upgrade vs uninstall is distinguished by `$1`.** opkg passes
`upgrade <version>` to `prerm`/`postrm` during an upgrade (verified on OpenWrt
23.05). The scripts use that to keep autostart links during an upgrade and to
stay quiet about "your settings were kept", which during an upgrade reads like a
warning about something that never happened.

**apk is not opkg, in two ways that bite.** Measured on the same router running
OpenWrt 25.12.5 with apk-tools 3.0.5:

1. *Different script names.* apk runs `.pre-install`/`.post-install` only on a
   fresh install; once the package exists it runs `.pre-upgrade`/`.post-upgrade`
   (apk-tools `src/database.c`: `.script = upgrade ? APK_SCRIPT_PRE_UPGRADE :
   APK_SCRIPT_PRE_INSTALL`). A package without those entries upgrades with **no
   scripts at all** — which is how a wrong-CPU package replaced a working binary
   silently, with `rc=0` and no output.
2. *A failing script cannot stop anything.* The script is executed from inside
   the extraction loop and its result is discarded
   (`apk_db_run_pending_script()` ignores the return of
   `apk_ipkg_run_script()`, which only sets `ipkg->broken_script`). `apk add`
   still exits 0.

So under apk the guard cannot refuse — it rescues instead: `preinst` copies the
working binary to `/tmp/veilbridge.rescue-bin` before the overwrite, and
`postinst` puts it back, restarts the panel and prints how to fix the package
database. Verified on the router: after installing the amd64 package on aarch64
the panel is running again on a restored arm64 binary, with a new PID.

## Not yet built (roadmap D1+)

Signed opkg feed, firmware images via ImageBuilder, first-run wizard.
