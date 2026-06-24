#!/usr/bin/env bash
# p5-e2e.sh — Phase-5 end-to-end check of the Ubuntu adapter, driven entirely
# through the product API (veilbridged), not the p4smoke harness.
#
# It runs ON vb-ubuntu (the binary + node.conf must already be there, delivered
# by deploy-stand.sh). Flow:
#   set-password → start daemon → login → import .conf → activate node →
#   poll status for handshake → probe egress (expect tunnel) → teardown.
#
# PASS = the probe reports ActualVia=tunnel with egress == the FI node IP,
# differing from the direct WAN. This proves the adapter's userspace engine +
# system.ProbePath work as a product, end to end.
#
# Usage (on vb-ubuntu):  ./p5-e2e.sh <conf-file> <expect-egress-ip>
set -euo pipefail

BIN="${BIN:-/home/vb/veilbridge-bin/veilbridged}"
CONF="${1:?usage: p5-e2e.sh <conf> <expect-egress-ip>}"
EXPECT="${2:?usage: p5-e2e.sh <conf> <expect-egress-ip>}"
PORT="${PORT:-18080}"
BASE="http://127.0.0.1:$PORT/api/v1"
# A path that does NOT yet exist — store.Load() treats a missing file as first
# run (Default()), whereas an empty mktemp file would fail JSON parse.
CFG="${TMPDIR:-/tmp}/vbcfg-$$-$RANDOM.json"
PASS="p5test"

cleanup() {
  [ -n "${DPID:-}" ] && sudo kill "$DPID" 2>/dev/null || true
  # config is root-owned (written by the sudo'd daemon) → remove with sudo
  sudo rm -f "$CFG" 2>/dev/null || true
}
trap cleanup EXIT

jqget() { python3 -c "import sys,json; print(json.load(sys.stdin)$1)"; }

echo "▶ set admin password (fresh config $CFG)"
sudo "$BIN" -config "$CFG" -set-password "$PASS"
# The daemon below also runs under sudo, so it can read the root-owned config.

echo "▶ start daemon on :$PORT (sudo — userspace TUN needs CAP_NET_ADMIN)"
sudo "$BIN" -config "$CFG" -listen "127.0.0.1:$PORT" >/tmp/veilbridged.log 2>&1 &
DPID=$!
# the sudo child is the real server; wait for the port to answer
for i in $(seq 1 20); do
  curl -fsS -o /dev/null "http://127.0.0.1:$PORT/" 2>/dev/null && break
  sleep 0.3
done

echo "▶ login"
TOKEN=$(curl -fsS -X POST "$BASE/auth/login" \
  -H 'Content-Type: application/json' -d "{\"password\":\"$PASS\"}" | jqget "['token']")
AUTH=(-H "Authorization: Bearer $TOKEN")

echo "▶ import .conf"
CONTENT=$(python3 -c "import json,sys; print(json.dumps(open('$CONF').read()))")
NODE_ID=$(curl -fsS -X POST "$BASE/nodes/import" "${AUTH[@]}" \
  -H 'Content-Type: application/json' \
  -d "{\"kind\":\"awg-config\",\"content\":$CONTENT}" | jqget "[0]['id']")
echo "  node id=$NODE_ID"

echo "▶ activate node"
curl -fsS -X POST "$BASE/nodes/$NODE_ID/activate" "${AUTH[@]}" -o /dev/null -w "  activate http=%{http_code}\n"

echo "▶ wait for handshake"
for i in $(seq 1 15); do
  ST=$(curl -fsS "$BASE/nodes/$NODE_ID/status" "${AUTH[@]}")
  AGE=$(echo "$ST" | jqget "['handshakeAgeSec']" 2>/dev/null || echo -1)
  if [ "$AGE" -ge 0 ] 2>/dev/null; then echo "  handshake age=${AGE}s rx=$(echo "$ST"|jqget "['rxBytes']") tx=$(echo "$ST"|jqget "['txBytes']")"; break; fi
  sleep 1
done

echo "▶ probe egress (expect tunnel)"
PROBE=$(curl -fsS -X POST "$BASE/system/probe" "${AUTH[@]}" \
  -H 'Content-Type: application/json' \
  -d '{"target":"1.1.1.1","expectedVia":"tunnel"}')
echo "  $PROBE"
ACTUAL=$(echo "$PROBE" | jqget "['actualVia']")
DETAIL=$(echo "$PROBE" | jqget "['detail']")

echo "----"
if [ "$ACTUAL" = "tunnel" ] && echo "$DETAIL" | grep -q "$EXPECT"; then
  echo "PASS: adapter routes through tunnel — $DETAIL"
  exit 0
else
  echo "FAIL: actualVia=$ACTUAL detail=$DETAIL (expected tunnel via $EXPECT)"
  echo "--- daemon log ---"; tail -20 /tmp/veilbridged.log
  exit 1
fi
