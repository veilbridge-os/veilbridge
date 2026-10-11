# internal/routing

VeilBridge's own routing layer (nftables rules, domain/subnet lists). v0.1 is
static rules; FakeIP arrives with M6 and health-check failover with M5.

- `routing.go` — `Generator.Render` turns `[]core.RouteRule` into an idempotent
  `nft -f` script (subnet rules → named sets per family/target; domain rules are
  comments in v0.1, pending FakeIP). Deterministic output (sorted) for diffable,
  testable scripts. Validates CIDRs before any apply.
- `apply.go` — `Generator.Apply` feeds the script to `nft -f -` (needs root).

The script only marks: `TunnelMark` is one bit, set with OR so other
packages' marks survive. Where marked traffic goes is the OS adapter's job:
on OpenWrt, the connection's own routing table and rules
(`internal/adapters/openwrt/tunnel_route.go`, #47).

Engine caveat: with the kernel engine LAN traffic is forwarded into the
tunnel's interface; with the userspace netstack engine the ruleset is a
correct declaration but userspace transit forwarding is not built yet. See the architecture notes in CONTRIBUTING.md
