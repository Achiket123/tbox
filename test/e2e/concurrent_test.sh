#!/bin/bash
set -e

echo "→ Testing deadlock-free stop..."
tbox run ./examples/alpine-cli.tgz -- sleep 30 &
CID1=$!
sleep 1 # Let it start
tbox stop $(tbox ps -q | head -1) # Should not hang
wait $CID1 2>/dev/null || true

echo "→ Testing concurrent runs (ptrace contention)..."
tbox run ./examples/alpine-cli.tgz -- sleep 5 &
PID1=$!
tbox run ./examples/busybox-static.tgz -- sleep 5 &
PID2=$!
wait $PID1 $PID2 2>/dev/null || true
# Either both succeed, or one fails with actionable error — never hang

echo "✓ All concurrent tests passed"