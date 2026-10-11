#!/bin/sh
# Runs before the files are removed (opkg prerm).
set -e

# opkg passes "upgrade <version>" here when this is an upgrade rather than an
# uninstall (verified on OpenWrt 23.05: prerm/postrm receive $1=upgrade).
# The service must stop either way, but only a real uninstall may drop the
# autostart links — removing them during an upgrade would leave a router that
# silently comes up without its panel if the install half fails.
if [ -x /etc/init.d/veilbridge ]; then
	# Leave a note when we stop a *running* panel during an upgrade. opkg has no
	# working unwind for prerm (libopkg/opkg_install.c:
	# prerm_upgrade_old_pkg_unwind() is a stub that returns 0), so if the new
	# package refuses itself in preinst, nothing would ever start the old panel
	# again. preinst reads this marker and repairs that.
	if [ "${1:-remove}" = "upgrade" ] &&
		/etc/init.d/veilbridge running >/dev/null 2>&1; then
		: > /tmp/veilbridge.was-running
	fi

	# procd prints "Command failed: ubus call service delete" on stdout when the
	# service is not currently registered; that is noise, not a failure.
	/etc/init.d/veilbridge stop >/dev/null 2>&1 || true

	if [ "${1:-remove}" != "upgrade" ]; then
		/etc/init.d/veilbridge disable >/dev/null 2>&1 || true
	fi
fi

# A real removal also takes out the routing the panel put in the kernel (#47):
# the rules that send marked traffic into the tunnel's table or refuse it, the
# two tables, and the table that marks the traffic. Left behind, addresses a
# rule sent to the tunnel would stay unreachable with no panel to say why.
# An upgrade keeps them: the new panel puts back the same ones on start.
if [ "${1:-remove}" != "upgrade" ]; then
	for fam in -4 -6; do
		for prio in 30089 30090 30100 30101 30102 30103; do
			i=0
			while [ $i -lt 8 ] && ip $fam rule del priority $prio >/dev/null 2>&1; do
				i=$((i + 1))
			done
		done
		ip $fam route flush table 147 >/dev/null 2>&1 || true
		ip $fam route flush table 148 >/dev/null 2>&1 || true
	done
	nft delete table inet veilbridge >/dev/null 2>&1 || true
fi

exit 0
