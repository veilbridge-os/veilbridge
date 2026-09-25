# internal/core

Abstract manager interfaces: VPNManager, RoutingManager, SystemManager and
NetworkManager (reads) — plus DeviceManager, still a roadmap stub until clients
and Wi-Fi arrive. Adapters implement these per platform. The API layer depends
only on these interfaces, never on a concrete adapter. See the architecture
notes in CONTRIBUTING.md

Two interfaces are deliberately kept apart from the managers above:

- `NetworkWriter` — changing network configuration. Separate from
  `NetworkManager` because reading is safe and writing is not: a getter cannot
  lock anybody out. Its methods only *stage*; the apply transaction commits.
- `VitalsReader` — the cheap local read for anything on a timer. `SystemInfo`
  is not that: filling it asks a third party for the device's public address,
  which is fine per request from a human and is an outbound stream when polled.

Layout:
- `types.go` — domain model (Node, RouteRule, SystemInfo, Vitals, WANConfig,
  NetworkInterface, WANStatus, ConfigChange, PathProbe, …).
- `managers.go` — manager interfaces, the `Adapter` bundle, the optional
  `LANReader`, `ErrNotImplemented`, `ErrNoWAN` and `ErrNoLAN` (a device with no
  uplink or no local network is a state, not a failure).
- `LANReader` / `LANWriter` are optional: the API asks for them by type
  assertion and answers 501 when the adapter lacks them, which is not the same
  as "this device has no local network" (404).
- `firewall.go` — the firewall in the panel's words: zones and their role,
  forwardings, port forwards, rules; `FirewallReader`/`FirewallWriter` are
  optional capabilities of an adapter, like `LANReader`.
- `refusal.go` — `FieldError` / `Refuse`: a refusal that names the request
  field it is about, returned by the API as `errors[].location`.
- `capabilities.go` — what a device can do, and why not when it cannot. The
  `reason` is written in the panel's own words; device nodes, package names and
  errno values live in `detail`.
- `apply.go` — the transaction: snapshot → stage → commit → confirm, with an
  automatic revert when nobody confirms in time.
- `mock/` — in-memory implementations so the API layer can be tested without a
  real OS or VPN engine (with compile-time interface assertions). It is also
  what `-demo` serves, which is why its data is kept plausible and inside the
  documentation address ranges.

Depends on the standard library only.
