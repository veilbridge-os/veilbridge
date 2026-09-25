#!/bin/sh
# VeilBridge installer for OpenWrt.
#
#   wget -qO- https://raw.githubusercontent.com/veilbridge-os/veilbridge/main/scripts/install.sh | sh
#
# It is a thin wrapper, not a third way to install: it picks the right package
# for this CPU, verifies the checksum, and hands over to opkg. Everything that
# decides how VeilBridge is installed (service, permissions, dependencies) lives
# in the package itself — see packaging/nfpm.yaml.
#
# By default it installs the latest stable release. Pre-releases are never
# "latest"; to try one, name it (see RELEASING.md for the channels):
#
#   wget -qO- https://raw.githubusercontent.com/veilbridge-os/veilbridge/main/scripts/install.sh | VB_VERSION=v0.2.0-alpha1 sh
#
# POSIX sh / ash: this runs on the router, where there is no bash.
set -eu

REPO="veilbridge-os/veilbridge"
VERSION="${VB_VERSION:-}"
case "$VERSION" in
	'') RELEASE="releases/latest/download" ;;
	v[0-9]*.[0-9]*.[0-9]*) RELEASE="releases/download/$VERSION" ;;
	*) echo "install: VB_VERSION must look like v0.2.0 or v0.2.0-alpha1, got '$VERSION'" >&2; exit 1 ;;
esac
# The case above admits only a tag-shaped value, so it cannot smuggle a path
# or a query into the URL.
case "$VERSION" in *[!A-Za-z0-9.-]*) echo "install: invalid VB_VERSION" >&2; exit 1 ;; esac
BASE="${VB_BASE_URL:-https://github.com/$REPO/$RELEASE}"
TMP="${TMPDIR:-/tmp}/veilbridge-install.$$"

die() { echo "install: $*" >&2; exit 1; }

cleanup() { rm -rf "$TMP"; }
trap cleanup EXIT INT TERM

# --- 1. Refuse early on the wrong platform, with a useful message ------------
[ -f /etc/openwrt_release ] || die "this installer is for OpenWrt.
  VeilBridge manages an OpenWrt router; on another OS the daemon refuses to
  start. To just look at the panel, run the binary with -demo."

case "$(uname -m)" in
	x86_64)          ARCH=amd64 ;;
	aarch64 | arm64) ARCH=arm64 ;;
	*) die "unsupported CPU: $(uname -m).
  Released builds cover x86_64 and aarch64. For other targets (mips, armv7)
  build from source — see the README." ;;
esac

# --- 2. Fetch --------------------------------------------------------------
# OpenWrt ships wget (uclient-fetch); curl may be absent. Use whichever exists.
if command -v curl >/dev/null 2>&1; then
	fetch() { curl -fsSL -o "$1" "$2"; }
elif command -v wget >/dev/null 2>&1; then
	fetch() { wget -qO "$1" "$2"; }
else
	die "neither curl nor wget found"
fi

# --- 2a. Which package manager is this? ------------------------------------
# OpenWrt ≤24.10 ships opkg and installs .ipk; 25.12 replaced it with apk-tools
# 3, which installs .apk and does not understand .ipk at all. Both formats are
# built from the same recipe and published in every release, so the only
# decision here is which file this router can actually consume.
if command -v apk >/dev/null 2>&1; then
	PM=apk
	EXT=apk
elif command -v opkg >/dev/null 2>&1; then
	PM=opkg
	EXT=ipk
else
	die "no supported package manager found (looked for apk and opkg).
  This installer targets OpenWrt; on 25.12 and later that means apk,
  on 24.10 and earlier opkg."
fi

mkdir -p "$TMP"
PKG="veilbridge_${ARCH}.${EXT}"

echo "install: downloading $PKG (${VERSION:-latest stable release})"
fetch "$TMP/$PKG" "$BASE/$PKG" || die "download failed: $BASE/$PKG"
fetch "$TMP/SHA256SUMS" "$BASE/SHA256SUMS" || die "download failed: SHA256SUMS"

# --- 3. Verify -------------------------------------------------------------
# busybox sha256sum has no --ignore-missing, so check exactly one line. Doing it
# any other way (verifying a renamed file) silently verifies nothing.
cd "$TMP"
grep " $PKG\$" SHA256SUMS > one.sum || die "no checksum for $PKG in SHA256SUMS"
sha256sum -c one.sum || die "checksum mismatch — refusing to install"

# --- 4. Install ------------------------------------------------------------
echo "install: $PM update (needed to resolve kmod-tun and nftables)"
"$PM" update >/dev/null 2>&1 || echo "install: $PM update failed, trying anyway" >&2

# --- 4a. Never go back silently -------------------------------------------
# The two package managers disagree here (measured): opkg refuses an older
# package ("Not downgrading") and still exits 0, while apk downgrades without
# a word when handed an explicit file. So someone on a pre-release who runs the
# plain one-liner would be moved back to the stable release on 25.12 and not on
# 24.10. Compare the versions with the package manager's own ordering and go
# back only when asked to.
DOWNGRADE="${VB_ALLOW_DOWNGRADE:-}"
OLDER=""
if [ "$PM" = apk ]; then
	CAND=$(tar -xzOf "$TMP/$PKG" .PKGINFO 2>/dev/null | sed -n 's/^pkgver = //p')
	INST=$(apk list -I veilbridge 2>/dev/null | sed -n 's/^veilbridge-\([^ ]*\) .*/\1/p')
	if [ -n "$CAND" ] && [ -n "$INST" ] && [ "$(apk version -t "$CAND" "$INST")" = "<" ]; then
		OLDER=1
	fi
else
	CAND=$(tar -xzOf "$TMP/$PKG" ./control.tar.gz 2>/dev/null | tar -xzOf - ./control 2>/dev/null | sed -n 's/^Version: //p')
	INST=$(opkg status veilbridge 2>/dev/null | sed -n 's/^Version: //p')
	if [ -n "$CAND" ] && [ -n "$INST" ] && opkg compare-versions "$CAND" '<<' "$INST"; then
		OLDER=1
	fi
fi
if [ -n "$OLDER" ] && [ -z "$DOWNGRADE" ]; then
	die "version $INST is installed and this release is older ($CAND).
  Nothing was changed. To go back on purpose, run again with
  VB_ALLOW_DOWNGRADE=1 (keep a copy of /etc/veilbridge first)."
fi

if [ "$PM" = apk ]; then
	# The package is not signed by a repository key (a signed feed is roadmap
	# D1), so apk needs to be told that installing this local file is intended.
	# The checksum was already verified against SHA256SUMS above, which is the
	# guarantee that actually matters here.
	apk add --allow-untrusted "$TMP/$PKG"
elif [ -n "$DOWNGRADE" ]; then
	opkg install --force-downgrade "$TMP/$PKG"
else
	opkg install "$TMP/$PKG"
fi

# --- 5. Verify the result, not the exit code ---------------------------------
# Neither package manager can be trusted to fail loudly: opkg exits 0 when it
# declines a downgrade, and apk ignores the result of package scripts. The
# release publishes the bare binary next to the packages, built from the same
# file, so the installed binary must match its checksum exactly.
want=$(awk -v f="veilbridged-linux-$ARCH" '$2 == f { print $1 }' SHA256SUMS)
got=$(sha256sum /usr/bin/veilbridged 2>/dev/null | cut -d' ' -f1)
if [ -n "$want" ] && [ "$got" != "$want" ]; then
	now=$(/usr/bin/veilbridged -version 2>/dev/null || echo "nothing runnable")
	die "the installed binary is not the one from this release.
  Installed now: $now
  If you asked for an older release, the package manager kept the newer one;
  run again with VB_ALLOW_DOWNGRADE=1 to go back on purpose."
fi
echo "install: installed $(/usr/bin/veilbridged -version)"
