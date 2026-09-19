#!/bin/sh
# Runs after the files are removed (opkg postrm).
#
# The config is deliberately NOT deleted: it holds the imported VPN nodes and
# their private keys, and `opkg upgrade` goes through remove+install. Silently
# wiping a user's tunnels during an upgrade would be unforgivable; leaving one
# directory behind after a deliberate uninstall is merely untidy, and the
# message below tells the user how to finish the job.
set -e

# opkg passes "upgrade <version>" during an upgrade (verified on OpenWrt 23.05).
# Saying "kept your settings" while upgrading would read like a warning about
# something that never happened, so stay quiet unless this is a real removal.
[ "${1:-remove}" = "upgrade" ] && exit 0

if [ -d /etc/veilbridge ]; then
	echo "veilbridge: kept /etc/veilbridge (VPN keys and settings)."
	echo "  Remove it yourself if you are done: rm -rf /etc/veilbridge"
fi

exit 0
