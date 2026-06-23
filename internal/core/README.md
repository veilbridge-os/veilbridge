# internal/core

Abstract manager interfaces: VPNManager, RoutingManager, SystemManager (MVP) plus
NetworkManager and DeviceManager (roadmap stubs). Adapters implement these per
platform. The API layer depends only on these interfaces, never on a concrete
adapter. See `CONTRIBUTING.md` §4.
