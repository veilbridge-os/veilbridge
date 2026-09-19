#!/bin/sh
# p8-e2e.sh — Phase-8 end-to-end check of the OpenWrt adapter (kernel engine),
# driven through the product API. POSIX sh / ash (OpenWrt has no bash), runs as
# root (no sudo), and uses no python3 (not installed on OpenWrt) — JSON is poked
# with sed. The daemon must be at /root/vb/veilbridged with the node .conf
# alongside, delivered by the deploy step.
#
# Proves the product path end to end on the target platform: the daemon detects
# openwrt, the KERNEL engine brings awg0 up, the handshake completes, and egress
# goes through the tunnel — the probe verifies egress by interface, since kernel
# engines have no Dialer.
#
# Usage:  ./p8-e2e.sh <conf> <expect-egress-ip>
set -eu

BIN=/root/vb/veilbridged
CONF="${1:?usage: p8-e2e.sh <conf> <expect-egress-ip>}"
EXPECT="${2:?usage: p8-e2e.sh <conf> <expect-egress-ip>}"
PORT=18080
BASE="http://127.0.0.1:$PORT/api/v1"
CFG="/tmp/vbcfg-openwrt.json"
PASS="p8test"

rm -f "$CFG"
DPID=""
cleanup() {
	[ -n "$DPID" ] && kill "$DPID" 2>/dev/null || true
	rm -f "$CFG"
	ip link show awg0 >/dev/null 2>&1 && ip link del awg0 2>/dev/null || true
}
trap cleanup EXIT INT TERM

# field VALUE from a flat JSON object: jval '"token"' extracts the string after it.
jval() { sed -n "s/.*\"$1\":\"\\([^\"]*\\)\".*/\\1/p"; }
# numeric field: jnum handshakeAgeSec
jnum() { sed -n "s/.*\"$1\":\\([0-9-]*\\).*/\\1/p"; }

echo "▶ platform detection"
[ -f /etc/openwrt_release ] && echo "  /etc/openwrt_release present (expect platform=openwrt)"

echo "▶ set admin password"
"$BIN" -config "$CFG" -set-password "$PASS"

echo "▶ start daemon on :$PORT"
"$BIN" -config "$CFG" -listen "127.0.0.1:$PORT" >/tmp/veilbridged.log 2>&1 &
DPID=$!
i=0
while [ $i -lt 20 ]; do
	curl -fsS -o /dev/null "http://127.0.0.1:$PORT/" 2>/dev/null && break
	i=$((i+1)); sleep 1
done

echo "▶ login"
TOKEN=$(curl -fsS -X POST "$BASE/auth/login" -H 'Content-Type: application/json' -d "{\"password\":\"$PASS\"}" | jval token)
[ -n "$TOKEN" ] || { echo "FAIL: no token"; tail -20 /tmp/veilbridged.log; exit 1; }

echo "▶ check platform via /system"
PLAT=$(curl -fsS "$BASE/system" -H "Authorization: Bearer $TOKEN" | jval platform)
echo "  platform=$PLAT"
[ "$PLAT" = "openwrt" ] || { echo "FAIL: platform=$PLAT, want openwrt"; exit 1; }

echo "▶ import .conf"
# JSON-escape the .conf into one string: backslashes, quotes, then newlines → \n.
ESC=$(sed -e 's/\\/\\\\/g' -e 's/"/\\"/g' "$CONF" | awk '{printf "%s\\n", $0}')
NODE_ID=$(curl -fsS -X POST "$BASE/nodes/import" -H "Authorization: Bearer $TOKEN" \
	-H 'Content-Type: application/json' \
	-d "{\"kind\":\"awg-config\",\"content\":\"$ESC\"}" | jval id)
echo "  node id=$NODE_ID"
[ -n "$NODE_ID" ] || { echo "FAIL: import returned no id"; tail -20 /tmp/veilbridged.log; exit 1; }

echo "▶ activate (kernel engine brings up awg0)"
curl -fsS -X POST "$BASE/nodes/$NODE_ID/activate" -H "Authorization: Bearer $TOKEN" -o /dev/null -w "  activate http=%{http_code}\n"

echo "▶ kernel interface present?"
# busybox ip has no -brief; match the inet line on the plain output instead.
if ip addr show awg0 2>/dev/null | grep -q 'inet '; then
	ip addr show awg0 | sed -n 's/^[[:space:]]*\(inet [0-9.]*\/[0-9]*\).*/  awg0 \1/p'
else
	echo "FAIL: awg0 not created (no inet)"; tail -20 /tmp/veilbridged.log; exit 1
fi

echo "▶ wait for handshake"
i=0
while [ $i -lt 15 ]; do
	AGE=$(curl -fsS "$BASE/nodes/$NODE_ID/status" -H "Authorization: Bearer $TOKEN" | jnum handshakeAgeSec)
	if [ -n "$AGE" ] && [ "$AGE" -ge 0 ] 2>/dev/null; then echo "  handshake age=${AGE}s"; break; fi
	i=$((i+1)); sleep 1
done

echo "▶ probe egress (expect tunnel, verified by interface)"
PROBE=$(curl -fsS -X POST "$BASE/system/probe" -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{"target":"1.1.1.1","expectedVia":"tunnel"}')
echo "  $PROBE"
ACTUAL=$(echo "$PROBE" | jval actualVia)

echo "----"
if [ "$ACTUAL" = "tunnel" ] && echo "$PROBE" | grep -q "$EXPECT"; then
	echo "PASS: same binary, kernel engine, OpenWrt — egress via $EXPECT"
	exit 0
else
	echo "FAIL: actualVia=$ACTUAL (expected tunnel via $EXPECT)"
	echo "--- daemon log ---"; tail -20 /tmp/veilbridged.log
	exit 1
fi
