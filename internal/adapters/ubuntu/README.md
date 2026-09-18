# internal/adapters/ubuntu

Ubuntu/Debian implementation of the core managers, all backed by one config store.

- `vpn.go` — `VPNManager`: parser + userspace netstack engine, nodes persisted in
  the store; one node active at a time; re-import is idempotent (stable ID).
- `routing.go` — `RoutingManager`: persists rules and renders/applies via
  `internal/routing` (declarative SetRules + Apply).
- `system.go` — `SystemManager`: `/proc` (uptime/mem/load), WAN IP, and `ProbePath`
  that compares egress through the tunnel vs direct (D-5). The egress resolver is
  injectable so probes are testable without the network.
- `adapter.go` — wires the managers; Network/Device are roadmap stubs.

Selected by `internal/adapters`.`New` when `/etc/openwrt_release` is absent.
See the architecture notes in CONTRIBUTING.md.
