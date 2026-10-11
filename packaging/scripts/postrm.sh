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

# The tunnel's firewall rules went with the package files; take them out of
# the live ruleset too.
# fw4 builds its ruleset from the uci view that includes a draft staged in
# the panel and not applied yet (measured), so reloading it here would put
# that draft live with no confirmation window. With a draft pending, the
# tunnel's rules wait for the next apply, which reloads the firewall anyway.
if [ -z "$(uci -q changes firewall)" ]; then
	fw4 -q reload >/dev/null 2>&1 || true
else
	echo "veilbridge: the firewall has changes that are not applied yet; the tunnel's"
	echo "  firewall rules take effect with the next apply (or: fw4 reload after it)."
fi

if [ -d /etc/veilbridge ]; then
	echo "veilbridge: kept /etc/veilbridge (VPN keys and settings)."
	echo "  Remove it yourself if you are done: rm -rf /etc/veilbridge"
fi

exit 0
