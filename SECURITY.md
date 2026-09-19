# Security Policy

VeilBridge sits on the network path of everything behind the router it manages,
so security reports get priority over features.

## Supported versions

The project is pre-1.0: **only the latest release and `main`** receive fixes.
There are no backports to older tags yet.

| Version | Supported |
| --- | --- |
| latest release / `main` | ✅ |
| anything older | ❌ |

## Reporting a vulnerability

**Do not open a public issue for a vulnerability.**

Use GitHub's private reporting:
[Security → Report a vulnerability](https://github.com/veilbridge-os/veilbridge/security/advisories/new).
It creates a private advisory visible only to the maintainers.

Please include:

- affected version (`veilbridged -version` output) and platform (OpenWrt / Debian / Ubuntu);
- what an attacker gains (read config, bypass auth, run code, reach the LAN…);
- reproduction steps or a proof-of-concept;
- anything you already know about the root cause.

What to expect:

| Step | Target |
| --- | --- |
| Acknowledgement | 72 hours |
| Initial assessment (severity, affected versions) | 7 days |
| Fix or mitigation plan | 30 days for high/critical |

Fixes are released together with an advisory that credits the reporter, unless
you ask to stay anonymous.

## Scope

In scope — the code in this repository:

- authentication and session handling of the API and web UI;
- privilege handling in the daemon (it runs as root to manage tunnels and firewall rules);
- adapter code that shells out to `uci`, `nft`, `ip`, `systemctl`;
- anything that could expose the admin password hash, private keys or VPN configs;
- release artifacts and the workflows that build them.

Out of scope:

- vulnerabilities in upstream projects (OpenWrt, Linux, amneziawg-go, Xray-core) —
  report those upstream; tell us if VeilBridge makes them materially worse;
- the panel speaking plain HTTP: this is documented, not a finding. Built-in TLS
  is on the roadmap; run it on a trusted LAN or behind a TLS-terminating proxy;
- findings that require an attacker who is already root on the device;
- missing hardening with no demonstrated impact, or raw scanner output.

## Notes for operators

- The admin password is stored bcrypt-hashed in the config file, never in plaintext.
- The config file holds VPN private keys: keep it `0600` and owned by root.
- Do not expose port 8080 to the internet. Remote access is a roadmap item and
  will come with TLS and a proper reverse-proxy story.
