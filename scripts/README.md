# scripts/

Dev tooling and on-hardware end-to-end checks for a VeilBridge test stand (an
OpenWrt device or an OpenWrt x86 VM). These are not part of CI — they need a
stand and real AmneziaWG nodes. Configure your own hosts via environment
variables; nothing here hardcodes a network.

## deploy-stand.sh

One command for the inner dev loop: **test → cross-compile → deliver to a test
VM**. Go runs on your dev machine; binaries are pure-Go (`CGO_ENABLED=0`), so
cross-compiling to `linux/amd64` needs no toolchain. If the VM sits in a NAT
behind a jump host, delivery hops through it, byte-for-byte via `cat`.

```sh
# point it at your stand:
export VB_VM_HOST=vb@vm-host        # the test VM (required)
export VB_HOP_HOST=me@jump-host     # optional jump host (omit for a direct VM)

scripts/deploy-stand.sh p4smoke                 # test + build + deliver
scripts/deploy-stand.sh p4smoke --no-test       # skip go test (tighter loop)
scripts/deploy-stand.sh veilbridged             # the daemon instead
```

Run on the VM afterwards (p4smoke needs CAP_NET_ADMIN → sudo, NOPASSWD on the VM):

```sh
ssh "$VB_VM_HOST" 'cd ~/veilbridge-bin && sudo ./p4smoke -conf node.conf -expect-egress <node-ip>'
```

### P4 risk gate — how to reproduce

1. Create a client on an AmneziaWG panel (panel on `:51821`), download its
   `.conf`, deliver it to `~/veilbridge-bin/node.conf` on the VM.
2. `scripts/deploy-stand.sh p4smoke`
3. Run p4smoke with `-expect-egress <the node's public IP>`.

PASS = egress through the tunnel equals the node IP and differs from the direct
host WAN (userspace engine heap stays around ~25 MiB).

## p8-e2e.sh

Phase-8 end-to-end check of the **OpenWrt adapter** (kernel engine), through the
same product API. POSIX sh / ash (no bash), runs as root (no sudo), and uses no
python3 (not on OpenWrt) — JSON is poked with sed. Prerequisites on the OpenWrt
target: `kmod-tun` (for `/dev/net/tun`) and `curl`.

```sh
# on the OpenWrt VM, with the binary + conf at /root/vb/:
cd /root/vb
sh p8-e2e.sh node.conf <expect-egress-ip>
```

Flow: confirm `/etc/openwrt_release` → start daemon → login → assert
`platform=openwrt` → import → activate (kernel engine brings up `awg0`) → assert
the interface has an inet addr → handshake → probe (verified **by interface**,
since kernel engines have no Dialer). PASS proves the product path end to end on
the target platform. Last green run: `awg0 inet 10.8.1.5/32`,
`tunnel egress=203.0.113.20 (direct=198.51.100.7)`, EXIT=0.

Note: busybox `ip` has no `-brief` flag — match `inet ` on plain `ip addr show`.

## Engine coverage

The kernel engine (the product path) is covered end to end by `p8-e2e.sh`. The
userspace netstack engine — the fallback for devices without `kmod-tun` — is
covered by the `p4smoke` harness above. The daemon picks the engine itself by
probing `/dev/net/tun` and reports the choice as `tunnelEngine` in
`GET /system`, so the fallback is reachable through the product too; a live
tunnel through it on a real router has not been run yet.

## install.sh

The one-command installer for the router (`wget -qO- … | sh`). It is a thin
wrapper, not a third installation path: it picks the build for the CPU, verifies
the checksum against `SHA256SUMS`, and calls `apk` (OpenWrt 25.12+) or `opkg`
(24.10 and earlier), whichever the device has. Everything that decides
*how* VeilBridge is installed — the procd service, config permissions, package
dependencies — lives in [`../packaging/nfpm.yaml`](../packaging/nfpm.yaml).

Verified on the OpenWrt stand: clean install prints the two remaining steps and
refuses to start without a password; upgrade keeps the config and brings the
service back; `opkg remove` stops the service, drops the autostart links and
deliberately keeps `/etc/veilbridge` (it holds VPN private keys); after a reboot
the panel comes up on its own. The same was later checked on a physical router
with both package managers (24.10.8 via `opkg`, 25.12.5 via `apk`).

## m1-rollback-e2e.sh

The risk gate for the apply transaction: it stages a change that cuts the
panel's own management link through the product API and proves the device
comes back by itself — once by staying silent past the confirmation window,
once by killing the daemon inside it so recovery has to come from the on-disk
journal. Usage and environment are documented at the top of the script. Run it
on a VM first and then on a physical router: a VM always has a hypervisor
console, which is exactly what hides a rollback that does not work.

## screenshots.mjs

Recaptures the README screenshots from `veilbridged -demo` in headless Chrome,
so every address in them comes from the documentation ranges.
