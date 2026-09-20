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

exit 0
