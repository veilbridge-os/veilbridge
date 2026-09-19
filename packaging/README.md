# packaging/

How VeilBridge is delivered to a router. One recipe is the source of truth for
the service, the permissions and the dependencies — not a wiki page and not a
hand-made file on someone's test stand.

- [`nfpm.yaml`](./nfpm.yaml) — the package recipe (nfpm builds `.ipk` for
  OpenWrt). `VERSION` and `VB_ARCH` come from the release workflow.
- [`openwrt/etc/init.d/veilbridge`](./openwrt/etc/init.d/veilbridge) — procd
  service, `START=95`, respawn with back-off, restarts when the config changes.
- [`scripts/`](./scripts) — `postinst` / `prerm` / `postrm`.

Build one locally (needs the binary in `dist/` first):

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -tags ui \
  -o dist/veilbridged-linux-arm64 ./cmd/veilbridged

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
family, and the CPU is checked *for real* in `postinst` by executing the binary:
a wrong download fails loudly instead of installing something that cannot run.

**Upgrade vs uninstall is distinguished by `$1`.** opkg passes
`upgrade <version>` to `prerm`/`postrm` during an upgrade (verified on OpenWrt
23.05). The scripts use that to keep autostart links during an upgrade and to
stay quiet about "your settings were kept", which during an upgrade reads like a
warning about something that never happened.

## Not yet built (roadmap D1+)

Signed opkg feed, firmware images via ImageBuilder, first-run wizard.
