#!/usr/bin/env bash
# deploy-stand.sh — one-command dev loop for the VeilBridge test stand.
#
# The stand VMs live in a NAT behind the hypervisor, so Go runs ONLY on the Mac:
# cross-compile here (CGO is disabled, pure Go → no toolchain needed), then
# stream the binary to the VM over a double SSH hop (Mac → the hypervisor → VM).
#
# Usage:
#   scripts/deploy-stand.sh p4smoke              # test + build + deliver
#   scripts/deploy-stand.sh p4smoke --run ARGS   # ...then run it on the VM
#   scripts/deploy-stand.sh veilbridged          # the daemon instead
#   scripts/deploy-stand.sh p4smoke --no-test    # skip `go test` (faster loop)
#
# Targets: p4smoke | veilbridged   (both go to vb-ubuntu)
set -euo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
CMD="${1:?usage: deploy-stand.sh <p4smoke|veilbridged> [--run ARGS...] [--no-test]}"
shift || true

# --- stand topology (vb-ubuntu, NAT behind the hypervisor) ---
HOP_HOST="user@hypervisor.example"          # the hypervisor
VM_USER_HOST="vb@vm.example"          # vb-ubuntu, reached FROM the hypervisor
SSH_OPTS="-o StrictHostKeyChecking=no -o ConnectTimeout=8"
REMOTE_DIR="/home/vb/veilbridge-bin"

DO_TEST=1
RUN=0
RUN_ARGS=()
while [ $# -gt 0 ]; do
  case "$1" in
    --no-test) DO_TEST=0; shift ;;
    --run)     RUN=1; shift; RUN_ARGS=("$@"); break ;;
    *) echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

case "$CMD" in p4smoke|veilbridged) ;; *) echo "unknown target: $CMD" >&2; exit 2 ;; esac

cd "$REPO"

if [ "$DO_TEST" -eq 1 ]; then
  echo "▶ go test ./... (on Mac)"
  go test ./...
fi

echo "▶ cross-compile $CMD → linux/amd64 (CGO off)"
OUT="$(mktemp -t "$CMD".XXXXXX)"
trap 'rm -f "$OUT"' EXIT
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o "$OUT" "./cmd/$CMD"
echo "  built $(wc -c < "$OUT" | tr -d ' ') bytes"

echo "▶ deliver → $VM_USER_HOST:$REMOTE_DIR/$CMD (via the hypervisor)"
# Binary-safe stream over the double hop; cat preserves bytes, no scp ProxyJump needed.
ssh $SSH_OPTS "$HOP_HOST" \
  "ssh $SSH_OPTS $VM_USER_HOST 'mkdir -p $REMOTE_DIR'"
cat "$OUT" | ssh $SSH_OPTS "$HOP_HOST" \
  "ssh $SSH_OPTS $VM_USER_HOST 'cat > $REMOTE_DIR/$CMD && chmod +x $REMOTE_DIR/$CMD && echo delivered: \$(wc -c < $REMOTE_DIR/$CMD) bytes'"

if [ "$RUN" -eq 1 ]; then
  echo "▶ run on vb-ubuntu: $CMD ${RUN_ARGS[*]}"
  # p4smoke needs CAP_NET_ADMIN for the userspace TUN → run under sudo (NOPASSWD).
  ssh $SSH_OPTS "$HOP_HOST" \
    "ssh $SSH_OPTS -t $VM_USER_HOST 'sudo $REMOTE_DIR/$CMD ${RUN_ARGS[*]}'"
fi

echo "✓ done"
