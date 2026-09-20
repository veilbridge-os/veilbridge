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
# POSIX sh / ash: this runs on the router, where there is no bash.
set -eu

REPO="veilbridge-os/veilbridge"
BASE="${VB_BASE_URL:-https://github.com/$REPO/releases/latest/download}"
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

echo "install: downloading $PKG"
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

if [ "$PM" = apk ]; then
	# The package is not signed by a repository key (a signed feed is roadmap
	# D1), so apk needs to be told that installing this local file is intended.
	# The checksum was already verified against SHA256SUMS above, which is the
	# guarantee that actually matters here.
	apk add --allow-untrusted "$TMP/$PKG"
else
	opkg install "$TMP/$PKG"
fi
