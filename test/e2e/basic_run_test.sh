#!/bin/bash
set -e

echo "→ Testing basic execution..."
output=$(tbox run ./examples/alpine-cli.tgz -- echo hello 2>&1)
[[ "$output" == *"hello"* ]] || { echo "FAIL: expected 'hello'"; exit 1; }

echo "→ Testing bind mount + env..."
mkdir -p ~/tbox_test
echo "test content" > ~/tbox_test/file.txt
output=$(tbox run ./examples/busybox-static.tgz \
  --env KEY=value \
  --bind ~/tbox_test:/data \
  -- sh -c 'echo $KEY && cat /data/file.txt' 2>&1)
[[ "$output" == *"value"* && "$output" == *"test content"* ]] || { echo "FAIL: bind/env"; exit 1; }

echo "→ Testing DNS resolution..."
output=$(tbox run ./examples/alpine-cli.tgz -- sh -c 'curl -s https://example.com | head -1' 2>&1)
[[ "$output" == *"<!doctype html>"* ]] || { echo "FAIL: DNS"; exit 1; }

echo "✓ All basic tests passed"