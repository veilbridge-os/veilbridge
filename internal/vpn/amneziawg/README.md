# internal/vpn/amneziawg

AmneziaWG VPN engine (amneziawg-go, MIT). The default and only engine in v0.1.

Two implementations of the `vpn.Engine` interface live here: a `kernelEngine`
(OpenWrt, kmod-amneziawg, transparent nftables forwarding) and a `netstackEngine`
(Ubuntu/test, userspace `netstack.CreateNetTUN`, no root). The adapter picks which
one based on platform — the TUN type is an adapter detail. See `CONTRIBUTING.md` §5.
