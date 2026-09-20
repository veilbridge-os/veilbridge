#!/bin/sh
# Runs after this package's files are written.
#
#   opkg  → postinst
#   apk   → .post-install (fresh) and .post-upgrade (package already installed)
set -e

BIN=/usr/bin/veilbridged
CONF=/etc/veilbridge/config.json
RESCUE=/tmp/veilbridge.rescue-bin

# Name the command that actually exists on this system, so the advice can be
# pasted: OpenWrt ≤24.10 has opkg, 25.12 and later have apk.
remove_cmd() {
	if command -v apk >/dev/null 2>&1; then
		echo "apk del veilbridge"
	else
		echo "opkg remove veilbridge"
	fi
}

# The install reached this far, so the marker prerm left for preinst has done
# its job; drop it before it can confuse a later run.
rm -f /tmp/veilbridge.was-running

# Rescue path — apk only. opkg honours a failing preinst and never gets here,
# but apk executes preinst from inside its extraction loop, ignores the exit
# code and unpacks the package anyway (measured: `apk add` of the amd64 package
# on aarch64 replaced the binary and still exited 0). preinst saw that coming
# and kept a copy of the working binary; put it back.
if [ -f "$RESCUE" ]; then
	cat "$RESCUE" > "$BIN" 2>/dev/null || true
	chmod 0755 "$BIN" 2>/dev/null || true
	rm -f "$RESCUE"
	/etc/init.d/veilbridge restart >/dev/null 2>&1 || true

	echo "veilbridge: this package does not match this CPU ($(uname -m))." >&2
	echo "  The package manager unpacked it anyway, so the working binary was" >&2
	echo "  restored from a copy and the panel is running again." >&2
	echo "  The package database now lists a package whose files are not" >&2
	echo "  installed — fix it: $(remove_cmd), then install the matching file" >&2
	echo "  (amd64 for x86_64, arm64 for aarch64)." >&2
	exit 1
fi

# Second line of defence for whatever preinst cannot foresee — a binary that
# matches uname but still does not run here (missing loader, corrupted
# download).
if ! "$BIN" -version >/dev/null 2>&1; then
	echo "veilbridge: $BIN does not run on this device." >&2
	echo "  Most likely the wrong CPU build was installed. This device is:" >&2
	echo "    $(uname -m)" >&2
	echo "  Remove this package ($(remove_cmd)) and install the" >&2
	echo "  matching file: amd64 for x86_64, arm64 for aarch64." >&2
	exit 1
fi

chmod 0700 /etc/veilbridge 2>/dev/null || true
[ -f "$CONF" ] && chmod 0600 "$CONF"

/etc/init.d/veilbridge enable 2>/dev/null || true

if [ -f "$CONF" ]; then
	# Upgrade path: the panel was already configured, so bring it back up.
	# Quietly — prerm already stopped it, so procd's stop step would otherwise
	# print "Command failed: ubus call service delete ... (Not found)", which
	# looks like a broken upgrade and is merely noise.
	/etc/init.d/veilbridge restart >/dev/null 2>&1 || true

	# Then say what actually happened instead of assuming it worked: `running`
	# reports the real procd state (verified: rc=1 stopped, rc=0 running).
	i=0
	while [ $i -lt 10 ]; do
		if /etc/init.d/veilbridge running >/dev/null 2>&1; then
			echo "veilbridge: $("$BIN" -version) — running"
			exit 0
		fi
		i=$((i + 1))
		sleep 1
	done

	echo "veilbridge: upgraded, but the service did not come up." >&2
	echo "  Check: logread -e veilbridge" >&2
	echo "  Start: /etc/init.d/veilbridge start" >&2
else
	# Fresh install: starting now would serve a panel nobody can log into,
	# so ask for the one thing that is missing instead of pretending.
	echo ""
	echo "veilbridge installed: $("$BIN" -version)"
	echo "Two steps to finish:"
	echo "  1) veilbridged -set-password '<choose-a-password>'"
	echo "  2) /etc/init.d/veilbridge start"
	echo "Then open http://$(uci -q get network.lan.ipaddr || echo '<router-ip>'):8080/"
	echo ""
fi

exit 0
