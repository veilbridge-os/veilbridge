#!/usr/bin/env bash
# m1-rollback-e2e.sh — the M1 risk gate: prove that a configuration change
# which cuts our own management access undoes itself, with nobody at the
# console.
#
# This script runs on the DEV MACHINE, not on the target. That is the whole
# point: a check that runs on the device would keep talking to 127.0.0.1 while
# the device is unreachable from the network, and would pass a rollback that
# does not work. The observer has to be on the far side of the break.
#
# What it proves, in order:
#
#   A. break-and-wait — stage a change that takes the management interface
#      away, apply it with a short confirmation window, verify the panel really
#      became unreachable (a gate that does not break anything is a green light
#      to nothing), then verify it comes back BY ITSELF and the configuration
#      is byte-identical to the value we recorded before.
#
#   B. break-and-kill — same break, but the daemon is killed while the change
#      is live, the way a bad config usually takes the daemon with it (reboot,
#      OOM, watchdog). Recovery then has to come from the on-disk journal at
#      the next start, not from a timer in a process that no longer exists.
#
# Staging goes through the product API (PUT /network/wan, M3.1), so the break
# travels the same path an operator's click takes. ssh is still used, but only
# to observe the device and to schedule the kill in scenario B — never to make
# or repair the change under test.
#
# Usage:
#   VB_SSH=root@192.0.2.10 VB_PANEL=192.0.2.10:8080 \
#   VB_PASSWORD='<admin-password>' VB_BREAK_IFACE=lanwan scripts/m1-rollback-e2e.sh
#
# Environment:
#   VB_SSH          user@host for staging the break            (required)
#   VB_PANEL        host:port of the panel as seen from here   (required)
#   VB_PASSWORD     admin password for /auth/login             (required)
#   VB_BREAK_IFACE  uci network section to break               (default: lan)
#   VB_WINDOW       confirmation window, seconds               (default: 60)
#   VB_SCENARIO     a | b | both                               (default: both)
#   VB_RESCUE_SSH   out-of-band ssh command for scenario B     (optional)
#                   e.g. "ssh -J hypervisor root@10.0.0.5" over a second
#                   interface. Used ONLY to kill the daemon while management
#                   is down; never to repair the configuration. A device with
#                   no second way in (a router reachable on one port only)
#                   falls back to a kill scheduled on the target before the
#                   break: the daemon still dies with the link down, which is
#                   the property under test.
#   VB_KILL_DELAY   seconds before the scheduled kill fires    (default: 25)
#   VB_OUTSIDE      address the target must reach after a revert (default:
#                   1.1.1.1). Skipped with a note if it was not reachable
#                   before the break either — an offline lab is not a failure.
#
# Exit code 0 only if every assertion held. Anything else means M1 is not done.
set -uo pipefail

SSH_HOST="${VB_SSH:?set VB_SSH=user@host of the target}"
PANEL="${VB_PANEL:?set VB_PANEL=host:port of the panel}"
PASSWORD="${VB_PASSWORD:?set VB_PASSWORD=admin password}"
BREAK_IFACE="${VB_BREAK_IFACE:-lan}"
WINDOW="${VB_WINDOW:-60}"
SCENARIO="${VB_SCENARIO:-both}"
RESCUE_SSH="${VB_RESCUE_SSH:-}"
KILL_DELAY="${VB_KILL_DELAY:-25}"
OUTSIDE="${VB_OUTSIDE:-1.1.1.1}"
# Scenario B's window. Short on purpose: it is the cost of a failed kill, paid
# in lockout time. It only has to outlast the observation budget below.
WINDOW_B="${VB_WINDOW_B:-180}"

BASE="http://$PANEL/api/v1"
SSH_OPTS=(-o StrictHostKeyChecking=no -o ConnectTimeout=5 -o BatchMode=yes)
# A bogus address from the documentation range (RFC 5737): applying it to the
# management interface is a realistic lockout and can collide with nothing.
BREAK_ADDR="198.51.100.9"

# Everything below is interpolated into commands that run on the target, so it
# is validated here rather than trusted: a section name with a shell
# metacharacter in it would be an injection into the router's root shell.
case "$BREAK_IFACE" in
	''|*[!A-Za-z0-9_]*) echo "VB_BREAK_IFACE must be a plain uci section name" >&2; exit 2 ;;
esac
for n in "$WINDOW" "$WINDOW_B" "$KILL_DELAY"; do
	case "$n" in
		''|*[!0-9]*) echo "VB_WINDOW, VB_WINDOW_B and VB_KILL_DELAY must be integers" >&2; exit 2 ;;
	esac
done

# An interrupted run is the one case where a human must know where the device
# went: the change is live, and only the daemon's own watchdog will undo it.
trap 'printf "\n\033[31minterrupted while a change may be live\033[0m\n"; \
      printf "  the device answers at %s until its window expires\n" "$BREAK_ADDR"; exit 130' INT TERM

FAILED=0
say()  { printf '\n\033[1m▶ %s\033[0m\n' "$*"; }
ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; }
bad()  { printf '  \033[31m✗ %s\033[0m\n' "$*"; FAILED=1; }
info() { printf '    %s\n' "$*"; }

on_target() { ssh "${SSH_OPTS[@]}" "$SSH_HOST" "$@"; }

# jval FIELD — pull a string field out of a flat JSON object without jq, which
# is not guaranteed on a dev machine either.
jval() { sed -n "s/.*\"$1\":\"\([^\"]*\)\".*/\1/p"; }

api() { # api METHOD PATH [BODY] — authenticated, short timeout
	local method="$1" path="$2" body="${3:-}"
	if [ -n "$body" ]; then
		curl -sS -m 10 -X "$method" "$BASE$path" \
			-H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d "$body"
	else
		curl -sS -m 10 -X "$method" "$BASE$path" -H "Authorization: Bearer $TOKEN"
	fi
}

panel_up() { curl -sS -m 3 -o /dev/null "http://$PANEL/api/v1/apply" 2>/dev/null; }

# wait_for_panel SECONDS — poll until the panel answers; echo how long it took,
# or "never".
wait_for_panel() {
	local budget="$1" waited=0
	while [ "$waited" -lt "$budget" ]; do
		if panel_up; then echo "$waited"; return 0; fi
		sleep 2; waited=$((waited + 2))
	done
	echo never; return 1
}

login() {
	TOKEN=$(curl -sS -m 10 -X POST "$BASE/auth/login" \
		-H 'Content-Type: application/json' -d "{\"password\":\"$PASSWORD\"}" | jval token)
	[ -n "$TOKEN" ]
}

# --- preflight ------------------------------------------------------------

say "preflight"
panel_up || { bad "panel at $PANEL does not answer before we break anything"; exit 1; }
ok "panel answers at $PANEL"

login || { bad "login failed — wrong VB_PASSWORD?"; exit 1; }
ok "logged in"

# 'reverted' and 'confirmed' are terminal states of an earlier transaction, not
# a pending one: the coordinator keeps the last outcome visible for the UI. Only
# a live or failed transaction blocks a new run.
PHASE=$(api GET /apply | jval phase)
case "$PHASE" in
	idle|reverted|confirmed) ok "apply phase is '$PHASE' — nothing pending" ;;
	awaiting_confirm) bad "an apply is awaiting confirmation right now — resolve it first"; exit 1 ;;
	*) bad "apply phase is '$PHASE' — refusing to start on a device in that state"; exit 1 ;;
esac

on_target true || { bad "no ssh to $SSH_HOST"; exit 1; }
ok "ssh to $SSH_HOST works"

# The value we will destroy and expect to get back, verbatim.
BEFORE=$(on_target "uci show network.$BREAK_IFACE" | sort)
[ -n "$BEFORE" ] || { bad "uci section network.$BREAK_IFACE does not exist on the target"; exit 1; }
info "network.$BREAK_IFACE before the break:"
echo "$BEFORE" | sed 's/^/      /'

STAGED=$(on_target "uci changes" | wc -l | tr -d ' ')
[ "$STAGED" = "0" ] || { bad "the target already has $STAGED staged uci changes — clean them first"; exit 1; }
ok "no staged uci changes on the target"

# The configuration coming back byte for byte is not the same as the device
# working again (#39). On the reference router the kernel once had no default
# route while netifd still listed one — the configuration was fine and the
# router could not reach anything. So the gate also records what is LIVE: the
# default route (only `via` and `dev`, which a DHCP renewal does not change)
# and whether an outside address answers.
route_now() {
	on_target "ip -4 route show default" 2>/dev/null |
		awk '{ for (i = 1; i <= NF; i++) if ($i == "via" || $i == "dev") printf "%s %s ", $i, $(i + 1); print "" }' |
		sed 's/ *$//' | sort
}
outside_ok() { on_target "ping -c 1 -W 3 $OUTSIDE" >/dev/null 2>&1; }

ROUTE_BEFORE=$(route_now)
[ -n "$ROUTE_BEFORE" ] || {
	bad "the target has no default route before the break — the gate could not tell a broken revert from this"
	exit 1
}
ok "default route before the break: $ROUTE_BEFORE"
if outside_ok; then
	OUTSIDE_BEFORE=1
	ok "the target reaches $OUTSIDE"
else
	OUTSIDE_BEFORE=0
	info "the target does not reach $OUTSIDE even now; that check is skipped"
fi

# --- helpers shared by both scenarios -------------------------------------

stage_break() {
	# Staged through the product's own API (M3.1): PUT /network/wan writes to
	# the uci staging area and commits nothing, so the break travels the exact
	# path an operator's click takes. Until M3 there was no manager that could
	# write, and this was done over ssh with `uci set` — a gate that proved the
	# transaction but skipped the code a person actually uses.
	local resp
	resp=$(api PUT /network/wan "{\"interface\":\"$BREAK_IFACE\",\"proto\":\"static\",\
\"address\":\"$BREAK_ADDR\",\"netmask\":\"255.255.255.0\"}" 2>/dev/null)
	case "$resp" in
	*'"dangerous":true'*) ;;
	*)
		bad "staging the break through the API failed: ${resp:-no answer}"
		# A half-written draft would be committed by whoever applies next.
		api DELETE /apply/changes >/dev/null 2>&1
		return 1
		;;
	esac

	# Verify it landed on the device. An API that answered 200 while staging
	# nothing would turn the whole run into an apply of an empty change set —
	# which "recovers" perfectly and proves nothing.
	local n
	n=$(on_target "uci changes network" | wc -l | tr -d ' ')
	if [ "${n:-0}" -lt 1 ]; then
		bad "the API reported a staged change the device does not have"
		api DELETE /apply/changes >/dev/null 2>&1
		return 1
	fi
	ok "staged via the product API: network.$BREAK_IFACE -> static $BREAK_ADDR ($n lines, not committed)"
}

# daemon_pid echoes the pid of the running daemon, and fails if the answer is
# not exactly one process: picking the first of several would let scenario B
# "prove" a kill by comparing two different processes.
daemon_pid() {
	local pids count
	pids=$(on_target "pgrep -f 'veilbridged -config'")
	count=$(printf '%s\n' "$pids" | grep -c '[0-9]')
	# Echo nothing when the answer is ambiguous. Reporting the failure is the
	# caller's job: this function runs inside a command substitution, and a
	# failure recorded in that subshell would never reach the exit code.
	[ "$count" = "1" ] || return 1
	printf '%s' "$pids" | tr -d ' \n'
}

# apply_snapshot_id echoes the snapshot id the panel currently reports. The
# gate compares it before and after: a phase of "reverted" left over from an
# earlier run must not be mistaken for this run's recovery.
apply_snapshot_id() { api GET /apply | jval snapshot_id; }

assert_restored() {
	local after
	after=$(on_target "uci show network.$BREAK_IFACE" | sort)
	if [ "$after" = "$BEFORE" ]; then
		ok "network.$BREAK_IFACE is byte-identical to what it was before"
	else
		bad "network.$BREAK_IFACE did NOT come back to its previous value"
		info "before: $(echo "$BEFORE" | tr '\n' ' ')"
		info "after:  $(echo "$after"  | tr '\n' ' ')"
	fi
	local staged
	staged=$(on_target "uci changes" | wc -l | tr -d ' ')
	if [ "$staged" = "0" ]; then
		ok "no staged changes left behind"
	else
		bad "$staged staged uci changes survived the revert — the next commit would re-apply the break"
	fi

	# netifd restores the route after the configuration, and on DHCP only once
	# a lease is back: give it the time a person would.
	local route="" waited=0
	while [ "$waited" -lt 30 ]; do
		route=$(route_now)
		[ "$route" = "$ROUTE_BEFORE" ] && break
		sleep 2
		waited=$((waited + 2))
	done
	if [ "$route" = "$ROUTE_BEFORE" ]; then
		ok "the default route is back ($route)"
	else
		bad "the default route did NOT come back — the configuration is restored and the device still cannot route"
		info "before: $ROUTE_BEFORE"
		info "now:    ${route:-none}"
	fi
	if [ "$OUTSIDE_BEFORE" = "1" ]; then
		if outside_ok; then
			ok "the target reaches $OUTSIDE again"
		else
			bad "the target no longer reaches $OUTSIDE after the revert"
		fi
	fi
}

# --- scenario A: break and wait for the watchdog --------------------------

scenario_a() {
	say "scenario A — apply a lockout, let the watchdog undo it"

	# The snapshot id before this run. A leftover "reverted" phase from an
	# earlier run must not be able to pass as this run's recovery.
	local snap_before
	snap_before=$(apply_snapshot_id)

	stage_break || return

	# The response to this request travels over the link the request itself
	# destroys, so losing it is normal and NOT a failure. The token is
	# recoverable from GET /apply once the panel is reachable again.
	local resp started
	started=$(date +%s)
	resp=$(curl -sS -m 15 -X POST "$BASE/apply" \
		-H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
		-d "{\"timeout_seconds\":$WINDOW}" 2>&1)
	if [ -n "$(echo "$resp" | jval token)" ]; then
		info "apply answered before the link died: phase=$(echo "$resp" | jval phase)"
	else
		info "no answer to POST /apply (expected: the reply had to cross the broken link)"
	fi

	# The gate is only meaningful if the break actually broke something.
	local blocked=0
	for _ in 1 2 3 4 5; do
		panel_up || { blocked=1; break; }
		sleep 2
	done
	if [ "$blocked" = "1" ]; then
		ok "panel became unreachable — the change really did cut management access"
	else
		bad "panel stayed reachable: this run proves NOTHING (break did not break)"
		on_target "uci revert network; /etc/init.d/network reload" >/dev/null 2>&1
		return
	fi

	# Nobody confirms. Nobody touches the console. We only wait.
	local back
	back=$(wait_for_panel $((WINDOW + 120)))
	if [ "$back" = "never" ]; then
		bad "panel never came back within $((WINDOW + 120))s - THE GATE FAILED"
		emergency_revert
		return
	fi
	local elapsed=$(( $(date +%s) - started ))
	ok "panel came back on its own after ${elapsed}s (window was ${WINDOW}s)"

	login || { bad "cannot log in after recovery"; return; }
	local phase
	phase=$(api GET /apply | jval phase)
	if [ "$phase" = "reverted" ]; then
		ok "apply phase is 'reverted'"
	else
		bad "apply phase is '$phase', expected 'reverted'"
	fi

	local snap_after
	snap_after=$(apply_snapshot_id)
	if [ -n "$snap_after" ] && [ "$snap_after" != "$snap_before" ]; then
		ok "the reported transaction is this run's ($snap_after, was ${snap_before:-none})"
	else
		bad "the panel still reports snapshot '${snap_after:-none}' from before this run"
	fi

	assert_restored
}

# --- scenario B: break, kill the daemon, recover from the journal ----------

# rescue runs a command on the target over the out-of-band channel. It exists
# so the daemon can be killed while the management link is down — it must
# never be used to put the configuration back, or the gate tests the operator
# instead of the product.
rescue() {
	local -a cmd
	read -r -a cmd <<<"$RESCUE_SSH"
	"${cmd[@]}" -o StrictHostKeyChecking=no -o ConnectTimeout=8 -o BatchMode=yes "$@"
}

# schedule_kill arranges for the daemon to be SIGKILLed on the target after
# KILL_DELAY seconds, detached from this ssh session so it survives the link
# dying with the break.
#
# Two mechanisms, because OpenWrt images differ: setsid is missing on 23.05 and
# present on 25.12, and busybox has no nohup at all. Whichever is used, the
# result is verified - the first version of this script swallowed a
# "nohup: not found" and then blamed the product for not recovering.
schedule_kill() {
	local pid="$1" launcher=""
	if on_target "command -v setsid >/dev/null"; then
		launcher=setsid
		on_target "setsid sh -c 'sleep $KILL_DELAY; kill -9 $pid' </dev/null >/tmp/vb-gate-kill.log 2>&1 &"
	elif on_target "command -v start-stop-daemon >/dev/null"; then
		launcher=start-stop-daemon
		on_target "start-stop-daemon -S -b -x /bin/sh -- -c 'sleep $KILL_DELAY; kill -9 $pid'"
	else
		bad "target has neither setsid nor start-stop-daemon - use VB_RESCUE_SSH"
		return 1
	fi

	# Verify the killer is really running before anything is committed.
	if on_target "pgrep -f 'sleep $KILL_DELAY' >/dev/null"; then
		ok "scheduled SIGKILL of pid $pid in ${KILL_DELAY}s (via $launcher)"
		return 0
	fi
	bad "the scheduled killer did not start (launcher: $launcher) - refusing to break the link"
	return 1
}

# emergency_revert asks the product to undo the pending change over whatever
# channel is still open, instead of leaving the device locked out until its
# window expires. It gives an order to the panel; it never edits config by hand
# - a gate that repairs the device itself has stopped testing the device.
emergency_revert() {
	local tok
	if [ -z "$RESCUE_SSH" ]; then
		info "no rescue channel: the device undoes this by itself when the window expires"
		return
	fi
	tok=$(rescue "wget -q -O - --header='Content-Type: application/json' \
		--post-data='{\"password\":\"$PASSWORD\"}' http://127.0.0.1:8080/api/v1/auth/login" | jval token)
	if [ -z "$tok" ]; then
		info "could not log in over the rescue channel; waiting out the window"
		return
	fi
	rescue "wget -q -O - --header='Authorization: Bearer $tok' --post-data='' \
		http://127.0.0.1:8080/api/v1/apply/revert" >/dev/null 2>&1
	info "asked the panel to revert over the rescue channel"
}

scenario_b() {
	say "scenario B — apply a lockout, kill the daemon, recover from the journal"

	if [ -n "$RESCUE_SSH" ]; then
		if ! rescue true; then
			bad "VB_RESCUE_SSH does not work — scenario B cannot run"
			return
		fi
		ok "out-of-band channel works (used only to kill the daemon)"
	else
		info "no VB_RESCUE_SSH — the kill is scheduled on the target, ${KILL_DELAY}s ahead of the break"
	fi

	login || { bad "cannot log in before scenario B"; return; }
	local phase
	phase=$(api GET /apply | jval phase)
	[ "$phase" = "idle" ] || info "phase before scenario B is '$phase' (a finished transaction, fine)"

	# The pid before the break. Without comparing it afterwards the scenario
	# can pass while nothing was ever killed — which is what happened on the
	# first run: busybox has no `nohup`, the scheduled kill died with its error
	# message swallowed, and the run blamed the product instead of itself.
	local pid_before snap_before
	pid_before=$(daemon_pid)
	if [ -z "$pid_before" ]; then
		bad "expected exactly one veilbridged process on the target, found none or several"
		return
	fi
	snap_before=$(apply_snapshot_id)
	info "daemon pid before the break: $pid_before"

	stage_break || return

	if [ -z "$RESCUE_SSH" ]; then
		if ! schedule_kill "$pid_before"; then
			# Bail out BEFORE committing: a break with no killer behind it is
			# a lockout with nothing to learn from.
			on_target "uci revert network" >/dev/null 2>&1
			return
		fi
	fi

	# The window outlasts the observation budget below, so a recovery we see
	# cannot be the timer. It is also deliberately short: if the kill fails,
	# this is exactly how long the device stays locked out.
	curl -sS -m 15 -X POST "$BASE/apply" \
		-H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
		-d "{\"timeout_seconds\":$WINDOW_B}" >/dev/null 2>&1
	info "applied with a ${WINDOW_B}s window — anything faster than that is not the timer"

	local blocked=0
	for _ in 1 2 3 4 5; do
		panel_up || { blocked=1; break; }
		sleep 2
	done
	if [ "$blocked" = "1" ]; then
		ok "panel unreachable again"
	else
		bad "panel stayed reachable — scenario B proves nothing"
		on_target "uci revert network; /etc/init.d/network reload" >/dev/null 2>&1
		return
	fi

	# Kill the daemon the way a bad config does: SIGKILL, no graceful shutdown,
	# no chance for the in-memory watchdog to fire. procd respawns it, and the
	# only thing that can still save the device is the on-disk journal.
	if [ -n "$RESCUE_SSH" ]; then
		if rescue "kill -9 $pid_before"; then
			ok "daemon killed with SIGKILL while the lockout was live"
		else
			bad "could not kill the daemon over the rescue channel"
			emergency_revert
			return
		fi
	else
		info "waiting for the scheduled kill to fire"
	fi

	# Observe for less than the window: a recovery inside this budget cannot be
	# the watchdog timer expiring.
	local budget=$((WINDOW_B - 40)) back
	back=$(wait_for_panel "$budget")
	if [ "$back" = "never" ]; then
		bad "panel did not come back within ${budget}s of the kill - journal recovery FAILED"
		info "the device is locked out; meanwhile it answers at $BREAK_ADDR"
		emergency_revert
		return
	fi
	ok "panel back after ${back}s, inside the ${WINDOW_B}s window — the journal, not the timer"

	# Prove the kill actually landed. A scenario B that recovers without the
	# daemon ever dying has quietly re-run scenario A.
	local pid_after
	pid_after=$(daemon_pid)
	if [ -n "$pid_after" ] && [ "$pid_after" != "$pid_before" ]; then
		ok "daemon pid changed $pid_before → $pid_after: it really was killed and respawned"
	else
		bad "daemon pid is still ${pid_after:-gone} — the kill never landed, this run proves nothing"
		info "killer log: $(on_target 'cat /tmp/vb-gate-kill.log 2>/dev/null' 2>&1)"
	fi

	login || { bad "cannot log in after recovery"; return; }
	phase=$(api GET /apply | jval phase)
	if [ "$phase" = "idle" ] || [ "$phase" = "reverted" ]; then
		ok "apply phase is '$phase' (nothing left pending)"
	else
		bad "apply phase is '$phase' — a transaction is still hanging"
	fi

	assert_restored
}

case "$SCENARIO" in
	a) scenario_a ;;
	b) scenario_b ;;
	*) scenario_a; scenario_b ;;
esac

say "result"
if [ "$FAILED" = "0" ]; then
	printf '  \033[32mM1 RISK GATE PASSED\033[0m on %s\n' "$PANEL"
	exit 0
fi
printf '  \033[31mM1 RISK GATE FAILED\033[0m on %s — do not start the network milestones\n' "$PANEL"
exit 1
