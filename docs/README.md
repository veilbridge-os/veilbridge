# docs

Public design notes and contributor documentation (English).
Internal planning lives outside this repository.

- [`embedding-notes.md`](./embedding-notes.md) — historical in-process engine
  experiments, the gVisor pin resolution and userspace RAM/tunnel-egress
  measurements. The product defaults to kernel TUN on OpenWrt; Xray is not
  shipped and is planned as a separate market app, not part of the daemon.
- [`emergency-access.md`](./emergency-access.md) — getting back into a router
  after a network change that stuck: `veilbridged -restore-network` over ssh,
  on a console or in failsafe mode.
- [`release-notes/`](./release-notes/) — the notes published with each
  release, one file per tag.
- [`img/`](./img/) — UI screenshots used in the README.
