#!/bin/sh
# Runs before the files are removed (opkg prerm).
set -e

# opkg passes "upgrade <version>" here when this is an upgrade rather than an
# uninstall (verified on OpenWrt 23.05: prerm/postrm receive $1=upgrade).
# The service must stop either way, but only a real uninstall may drop the
# autostart links — removing them during an upgrade would leave a router that
# silently comes up without its panel if the install half fails.
if [ -x /etc/init.d/veilbridge ]; then
	# procd prints "Command failed: ubus call service delete" on stdout when the
	# service is not currently registered; that is noise, not a failure.
	/etc/init.d/veilbridge stop >/dev/null 2>&1 || true

	if [ "${1:-remove}" != "upgrade" ]; then
		/etc/init.d/veilbridge disable >/dev/null 2>&1 || true
	fi
fi

exit 0
