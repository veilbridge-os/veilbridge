#!/usr/bin/env bash
# deploy-stand.sh — one-command dev loop for a VeilBridge test stand.
#
# Cross-compiles on your dev machine (CGO disabled, pure Go → no toolchain) and
# streams the binary to a test VM. If the VM sits in a NAT behind a jump host,
# the delivery hops through it (dev → jump → VM), byte-for-byte via `cat`.
#
# Configure the stand via environment variables (no hardcoded hosts):
#   VB_VM_HOST   user@host of the test VM            (required)
#   VB_HOP_HOST  user@host of the jump host          (optional; direct if unset)
#   VB_REMOTE_DIR target dir on the VM               (default: ~/veilbridge-bin)
#
# Usage:
#   VB_VM_HOST=vb@10.0.0.5 scripts/deploy-stand.sh p4smoke
#   VB_VM_HOST=vb@vm VB_HOP_HOST=me@jump scripts/deploy-stand.sh veilbridged
#   scripts/deploy-stand.sh p4smoke --run -conf node.conf -expect-egress <ip>
#   scripts/deploy-stand.sh p4smoke --no-test    # skip `go test` (faster loop)
#
# Targets: p4smoke | veilbridged
set -euo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
CMD="${1:?usage: deploy-stand.sh <p4smoke|veilbridged> [--run ARGS...] [--no-test]}"
shift || true

# --- stand topology (from the environment) ---
VM_USER_HOST="${VB_VM_HOST:?set VB_VM_HOST=user@host of the test VM}"
HOP_HOST="${VB_HOP_HOST:-}"   # optional jump host; empty → connect directly
SSH_OPTS="-o StrictHostKeyChecking=no -o ConnectTimeout=8"
REMOTE_DIR="${VB_REMOTE_DIR:-\$HOME/veilbridge-bin}"

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

# Run a command on the VM, hopping through the jump host if one is configured.
vm_ssh() { # vm_ssh [-t] <remote-command>
  local tflag=""
  [ "$1" = "-t" ] && { tflag="-t"; shift; }
  if [ -n "$HOP_HOST" ]; then
    ssh $SSH_OPTS $tflag "$HOP_HOST" "ssh $SSH_OPTS $tflag $VM_USER_HOST '$1'"
  else
    ssh $SSH_OPTS $tflag "$VM_USER_HOST" "$1"
  fi
}
# Stream stdin into a file on the VM (binary-safe; no scp ProxyJump needed).
vm_put() { # vm_put <remote-path>
  if [ -n "$HOP_HOST" ]; then
    ssh $SSH_OPTS "$HOP_HOST" "ssh $SSH_OPTS $VM_USER_HOST 'cat > $1'"
  else
    ssh $SSH_OPTS "$VM_USER_HOST" "cat > $1"
  fi
}

if [ "$DO_TEST" -eq 1 ]; then
  echo "▶ go test ./..."
  go test ./...
fi

echo "▶ cross-compile $CMD → linux/amd64 (CGO off)"
OUT="$(mktemp -t "$CMD".XXXXXX)"
trap 'rm -f "$OUT"' EXIT
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o "$OUT" "./cmd/$CMD"
echo "  built $(wc -c < "$OUT" | tr -d ' ') bytes"

echo "▶ deliver → $VM_USER_HOST:$REMOTE_DIR/$CMD"
vm_ssh "mkdir -p $REMOTE_DIR"
cat "$OUT" | vm_put "$REMOTE_DIR/$CMD"
vm_ssh "chmod +x $REMOTE_DIR/$CMD && echo delivered: \$(wc -c < $REMOTE_DIR/$CMD) bytes"

if [ "$RUN" -eq 1 ]; then
  echo "▶ run on VM: $CMD ${RUN_ARGS[*]}"
  # p4smoke needs CAP_NET_ADMIN for the userspace TUN → run under sudo (NOPASSWD).
  vm_ssh -t "sudo $REMOTE_DIR/$CMD ${RUN_ARGS[*]}"
fi

echo "✓ done"
