# docs

Public design notes and contributor documentation (English).
Internal planning lives outside this repository.

- [`embedding-notes.md`](./embedding-notes.md) — how the AmneziaWG (and, from
  later, Xray) engines are embedded in-process, the gVisor pin resolution, and the
  verified RAM / tunnel-egress measurements.
- [`emergency-access.md`](./emergency-access.md) — getting back into a router
  after a network change that stuck: `veilbridged -restore-network` over ssh,
  on a console or in failsafe mode.
- [`release-notes/`](./release-notes/) — the notes published with each
  release, one file per tag.
- [`img/`](./img/) — UI screenshots used in the README.
