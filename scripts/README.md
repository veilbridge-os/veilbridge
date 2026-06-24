# scripts/

Dev tooling for the VeilBridge test stand (the libvirt VMs on the hypervisor).

## deploy-stand.sh

One command for the inner dev loop: **test on Mac → cross-compile → deliver to the
stand VM**. Go runs only on the Mac (the VMs sit in a NAT behind the hypervisor and may
carry an older Go); binaries are pure-Go (`CGO_ENABLED=0`), so cross-compiling to
`linux/amd64` needs no toolchain. Delivery streams the binary over a double SSH
hop (Mac → the hypervisor → vb-ubuntu) byte-for-byte via `cat`.

```sh
scripts/deploy-stand.sh p4smoke                 # test + build + deliver
scripts/deploy-stand.sh p4smoke --no-test       # skip go test (tighter loop)
scripts/deploy-stand.sh veilbridged             # the daemon instead
```

Run on the VM afterwards (p4smoke needs CAP_NET_ADMIN → sudo, NOPASSWD on the VM):

```sh
ssh user@hypervisor.example \
  "ssh vb@vm.example 'cd ~/veilbridge-bin && sudo ./p4smoke -conf node.conf -expect-egress <node-ip>'"
```

### P4 risk gate — how to reproduce

1. Create a client on an AmneziaWG panel (e.g. the NL node, panel `:51821`),
   download its `.conf`, deliver it to `~/veilbridge-bin/node.conf` on vb-ubuntu.
2. `scripts/deploy-stand.sh p4smoke`
3. Run p4smoke with `-expect-egress <the node's public IP>`.

PASS = egress through the tunnel equals the node IP and differs from the direct
host WAN. Last green run: egress `203.0.113.10` (NL) vs direct `198.51.100.7`,
userspace engine heap ~25 MiB.

## p5-e2e.sh

Phase-5 end-to-end check of the **Ubuntu adapter** through the product API
(`veilbridged`), not the p4smoke harness. Runs on vb-ubuntu; needs the daemon
binary + a node `.conf` already delivered there.

```sh
# on vb-ubuntu, after deploy-stand.sh veilbridged:
cd ~/veilbridge-bin
bash p5-e2e.sh fi-node.conf 203.0.113.20    # <conf> <expect-egress-ip>
```

Flow: set-password → start daemon → login (JWT) → import .conf → activate →
poll status for handshake → `POST /system/probe` (expect tunnel) → teardown.
PASS = probe reports `actualVia=tunnel` with egress == the node IP, differing
from the direct WAN. Last green run (FI node): `tunnel egress=203.0.113.20
(direct=198.51.100.7)`, EXIT=0.

Note: the daemon and probe both need root (userspace TUN → CAP_NET_ADMIN); the
script uses sudo (NOPASSWD on the VM). Pass a config path that does **not** yet
exist — `store.Load()` treats a missing file as first run, but an empty file
fails JSON parse.

Stand topology and credentials live in the project memory
(`project-the hypervisor-veilbridge-test-vms`), not in the repo.
