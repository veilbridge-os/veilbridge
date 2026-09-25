# Releasing VeilBridge

How a version is planned, when it is allowed to ship, and how it is cut and
verified. A release here is a promise about a router in somebody's home, so the
bar is "proven on hardware", not "the build is green".

## Planning: versions, epics, tasks

- **Milestones are versions** (`v0.2.0`, `v0.3.0`, …). Each lists what it has to
  contain; see [milestones](https://github.com/veilbridge-os/veilbridge/milestones).
- **Epics** (issue type *Epic*) are milestone-sized bodies of work; their
  sub-issues show progress. **Tasks** are broken down only for the near
  horizon — a distant epic carries scope and acceptance criteria until its turn.
- The board is the public [VeilBridge Roadmap](https://github.com/orgs/veilbridge-os/projects/1)
  project: *Current release* for what is being worked on, *Epics* for the whole
  map, *Roadmap* for the timeline.
- **There are no dates on purpose.** A version ships when every epic in its
  milestone is closed and proven, not when a calendar says so.

| Version | Contents |
| --- | --- |
| `v0.1` | ✅ AmneziaWG engine, own routing, dashboard, OpenWrt adapter |
| `v0.2.0` | platform layer with self-reverting apply, rebuilt panel, router network (uplink, LAN/DHCP, firewall, routes, emergency access) |
| `v0.3.0` | devices and Wi-Fi, exit policies and failover, VPN client protocols; first firmware image; signed feed |
| `v0.4.0` | DNS interception, FakeIP and domain routing, filters |
| `v0.5.0` | app platform, SDK and market; VPN features as apps |
| `v0.6.0` | parity apps: VPN servers, USB, storage, modems, IPTV, QoS |
| `v1.0.0` | TLS, users and roles, updates from the UI, remote access |

## Version numbers

[Semantic versioning](https://semver.org/). While the major version is `0`, a
minor bump is a milestone above and may change the API; a patch is fixes only.

**Pre-release tags have no dot inside the suffix:** `v0.2.0-alpha1`, not
`v0.2.0-alpha.1`. The tag becomes the package version on two package managers,
and apk-tools 3 rejects `0.2.0_alpha.1` as an invalid version (measured on
OpenWrt 25.12.5), while `0.2.0_alpha1` (apk) and `0.2.0~alpha1` (opkg) are valid
and both sort after `0.1.x` and before `0.2.0`. The release workflow refuses any
tag that is not `vX.Y.Z` or `vX.Y.Z-(alpha|beta|rc)N`. Keep `N` below 10: semver
compares `alpha10` and `alpha2` as text.

## Channels

| Channel | What it means | How people get it |
| --- | --- | --- |
| **stable** `vX.Y.Z` | the milestone is closed and proven | `install.sh` with no arguments; the release is marked *Latest* |
| **rc** `vX.Y.Z-rcN` | scope complete, only fixes from here | `VB_VERSION=vX.Y.Z-rcN` |
| **beta** `vX.Y.Z-betaN` | scope complete, API for this version frozen, known rough edges | `VB_VERSION=…` |
| **alpha** `vX.Y.Z-alphaN` | the milestone is in progress; what is on `main` works on hardware, but the version is not complete | `VB_VERSION=…` |

A pre-release is **never** marked *Latest*: the one-line installer and the
README download from `releases/latest`, so doing that would ship an alpha to
everyone. The workflow sets `prerelease` and `make_latest` from the tag.

```sh
# on the router — a specific version instead of the latest stable one
wget -qO- https://raw.githubusercontent.com/veilbridge-os/veilbridge/main/scripts/install.sh | VB_VERSION=v0.2.0-alpha1 sh
```

## Before tagging

All of these, on the exact commit that will be tagged:

1. **CI is green** on that commit on `main`.
2. **The risk gate passed** on a VM *and* on a physical router, if anything in
   `internal/adapters` or `internal/core` changed since the previous tag:
   `scripts/m1-rollback-e2e.sh` (see its header). A VM alone is not enough — it
   always has a hypervisor console, which hides a rollback that does not work.
3. **Release notes are written** in `docs/release-notes/<tag>.md`: what is new
   for a person with a router, what is known not to work, how to upgrade and how
   to go back. The workflow refuses to publish without this file; the generated
   commit list is appended below it.
4. **`CHANGELOG.md` is updated**: the `Unreleased` section becomes the version.
5. **No private infrastructure leaked**: no real addresses of test machines,
   passwords or hostnames anywhere in the tree. Examples use the documentation
   ranges from RFC 5737 (`192.0.2.0/24`, `198.51.100.0/24`, `203.0.113.0/24`).
6. For a **stable** release: every epic in the milestone is closed.

## Cutting a release

```sh
git switch main && git pull --ff-only
# notes + changelog committed and pushed, CI green on this commit
git tag -a v0.2.0-alpha1 -m "v0.2.0-alpha1"
git push origin v0.2.0-alpha1
gh run watch "$(gh run list --workflow Release --limit 1 --json databaseId --jq '.[0].databaseId')"
```

The [release workflow](.github/workflows/release.yml) builds the binaries with
the UI embedded, checks that the binary reports the tag, serves the embedded UI
in demo mode, builds `.ipk` and `.apk` for amd64 and arm64, checks their layout
and CPU guard, writes `SHA256SUMS` and publishes the release.

## After publishing — verify, do not assume

1. The release is marked correctly: pre-release for `-alphaN/-betaN/-rcN`, and
   *Latest* still points at the newest stable version.
2. Seven assets: two binaries, four packages, `SHA256SUMS`.
3. **Install it the way users will**, with `install.sh`, on both package
   managers: an OpenWrt 24.10-or-older target (opkg) and a 25.12 target (apk).
   `veilbridged -version` reports the tag, the package manager lists the expected
   package version, an upgrade keeps `/etc/veilbridge`, and the panel signs in.
4. Close the milestone (stable only) and move the finished items on the board.

## When a release is bad

Never delete or move a tag somebody may have installed. Publish a fixed patch
(or the next pre-release) and put a warning at the top of the bad release's
notes pointing to it. If the problem is only in the published assets, rebuild
from the same tag with the workflow's manual trigger — note that a rebuild of a
stable tag marks it *Latest* again, so rebuild only the newest stable one.

## Not yet in place

- Signed package feed, so routers update through their own package manager —
  [epic #22](https://github.com/veilbridge-os/veilbridge/issues/22).
- Firmware images — [epic #23](https://github.com/veilbridge-os/veilbridge/issues/23).
- Signed tags.
