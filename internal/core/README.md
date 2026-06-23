# internal/core

Abstract manager interfaces: VPNManager, RoutingManager, SystemManager (MVP) plus
NetworkManager and DeviceManager (roadmap stubs). Adapters implement these per
platform. The API layer depends only on these interfaces, never on a concrete
adapter. See `CONTRIBUTING.md` §4.

Layout:
- `types.go` — domain model (Node, RouteRule, SystemInfo, PathProbe, …).
- `managers.go` — manager interfaces, the `Adapter` bundle, and `ErrNotImplemented`.
- `mock/` — in-memory implementations so the API layer can be tested without a
  real OS or VPN engine (with compile-time interface assertions).

Depends on the standard library only.
