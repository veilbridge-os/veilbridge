# internal/routing

VeilBridge's own routing layer (nftables rules, domain/subnet lists). v0.1 is
static rules; FakeIP and health-check failover arrive in v0.2.

- `routing.go` — `Generator.Render` turns `[]core.RouteRule` into an idempotent
  `nft -f` script (subnet rules → named sets per family/target; domain rules are
  comments in v0.1, pending FakeIP). Deterministic output (sorted) for diffable,
  testable scripts. Validates CIDRs before any apply.
- `apply.go` — `Generator.Apply` feeds the script to `nft -f -` (needs root).

Engine caveat: marks/routes transparently forward LAN traffic with the kernel
engine (OpenWrt); with the userspace netstack engine (Ubuntu v0.1) the ruleset is
a correct declaration but transit forwarding is v0.2. See the architecture notes in CONTRIBUTING.md
