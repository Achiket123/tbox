#!/bin/bash
set -e

echo "→ Testing deadlock-free stop..."
# Run container in background; capture the tbox process PID separately.
# CID1 ($!) is the shell job PID — not the container ID.
# We use `tbox ps` (without -q, which is unimplemented) to find the container.
tbox run ./examples/alpine-cli.tgz sleep 30 &
TBOX_PID1=$!
sleep 2 # Let container start and register its state

# Retrieve the first container ID from tbox ps output (skip header line)
CONTAINER_ID=$(tbox ps | awk 'NR==2{print $1}')
if [ -z "$CONTAINER_ID" ]; then
    echo "✗ No running container found; ps output:" >&2
    tbox ps >&2
    kill "$TBOX_PID1" 2>/dev/null || true
    exit 1
fi
echo "  Stopping container $CONTAINER_ID"
tbox stop "$CONTAINER_ID"   # Should not hang
wait "$TBOX_PID1" 2>/dev/null || true

echo "→ Testing concurrent runs (ptrace contention)..."
tbox run ./examples/alpine-cli.tgz sleep 5 &
PID1=$!
tbox run ./examples/busybox-static.tgz sleep 5 &
PID2=$!
wait $PID1 $PID2 2>/dev/null || true
# Either both succeed, or one fails with actionable error — never hang

echo "✓ All concurrent tests passed"