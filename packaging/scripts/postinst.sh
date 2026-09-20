#!/bin/sh
# Runs after the files are unpacked (opkg postinst).
set -e

BIN=/usr/bin/veilbridged
CONF=/etc/veilbridge/config.json

# The first guard is preinst, which refuses a wrong-CPU package before any file
# is unpacked (see packaging/scripts/preinst.sh.in). This is the second line of
# defence for whatever preinst cannot foresee — a binary that matches uname but
# still does not run here (missing loader, wrong libc, corrupted download).
#
# The install reached this far, so the marker prerm left for preinst has done
# its job; drop it before it can confuse a later run.
rm -f /tmp/veilbridge.was-running

if ! "$BIN" -version >/dev/null 2>&1; then
	echo "veilbridge: $BIN does not run on this device." >&2
	echo "  Most likely the wrong CPU build was installed. This device is:" >&2
	echo "    $(uname -m)" >&2
	echo "  Remove this package (opkg remove veilbridge) and install the" >&2
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
