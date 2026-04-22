#!/bin/sh
# build-busybox-static.sh
# Builds examples/busybox-static.tgz for tbox on Termux (arm64, no root)
# Uses Alpine Linux's static busybox binary (true static, musl-linked)

set -e

TBOX_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOTFS="$TBOX_DIR/examples/busybox-static/rootfs"
OUT="$TBOX_DIR/examples/busybox-static.tgz"

echo "==> Cleaning up old rootfs..."
rm -rf "$ROOTFS"

echo "==> Creating rootfs directory structure..."
mkdir -p "$ROOTFS/bin" "$ROOTFS/etc" "$ROOTFS/proc" \
         "$ROOTFS/sys" "$ROOTFS/dev" "$ROOTFS/tmp" "$ROOTFS/root"

# ── Download static busybox from Alpine apk ────────────────────────────────
# Alpine's busybox-static package contains /bin/busybox.static — truly static,
# musl-linked, aarch64. We extract just that one file.

ALPINE_MIRROR="https://dl-cdn.alpinelinux.org/alpine/v3.19/main/aarch64"
PKG="busybox-static-1.36.1-r21.apk"
TMPDIR="$TBOX_DIR/.busybox-dl"

echo "==> Downloading Alpine busybox-static apk..."
rm -rf "$TMPDIR" && mkdir -p "$TMPDIR"
cd "$TMPDIR"

# pkg install wget if needed
command -v wget >/dev/null 2>&1 || pkg install -y wget

wget -q --show-progress "$ALPINE_MIRROR/$PKG" -O busybox-static.apk

echo "==> Extracting busybox binary from apk..."
# apk files are gzipped tarballs
tar -xzf busybox-static.apk ./bin/busybox.static 2>/dev/null || \
tar -xzf busybox-static.apk bin/busybox.static 2>/dev/null || {
    # Fallback: list contents and find it
    echo "Listing apk contents..."
    tar -tzf busybox-static.apk | grep busybox
    echo "ERROR: Could not find busybox.static in apk"
    exit 1
}

STATIC_BIN="$(find "$TMPDIR" -name 'busybox.static' | head -1)"
echo "==> Found: $STATIC_BIN"

cp "$STATIC_BIN" "$ROOTFS/bin/busybox"
chmod 755 "$ROOTFS/bin/busybox"
cd "$TBOX_DIR"
rm -rf "$TMPDIR"

# ── Verify ────────────────────────────────────────────────────────────────
echo "==> Verifying binary..."
file "$ROOTFS/bin/busybox"
file "$ROOTFS/bin/busybox" | grep -q "aarch64" || {
    echo "ERROR: Not an aarch64 binary."
    exit 1
}
ldd "$ROOTFS/bin/busybox" 2>&1 | grep -q "not a dynamic" && \
    echo "    -> Statically linked. OK" || {
    echo "    -> WARNING: not fully static, checking..."
    ldd "$ROOTFS/bin/busybox" 2>/dev/null || true
}

# ── Symlinks ──────────────────────────────────────────────────────────────
echo "==> Creating applet symlinks..."
cd "$ROOTFS/bin"
for cmd in sh ash ls cat echo pwd env id whoami date sleep \
           mkdir rm cp mv grep sed awk cut tr head tail wc \
           find chmod chown touch ln stat df du ps kill; do
    ln -sf busybox "$cmd" 2>/dev/null || true
done
cd "$TBOX_DIR"

# ── /etc files ────────────────────────────────────────────────────────────
echo "==> Writing /etc files..."
echo 'nameserver 8.8.8.8' > "$ROOTFS/etc/resolv.conf"
printf 'root:x:0:0:root:/root:/bin/sh\n' > "$ROOTFS/etc/passwd"
printf 'root:x:0:\n' > "$ROOTFS/etc/group"

# ── tbox.json ─────────────────────────────────────────────────────────────
echo "==> Copying tbox.json manifest..."
cp "$TBOX_DIR/examples/busybox-static/tbox.json" "$ROOTFS/tbox.json"

# ── Pack ──────────────────────────────────────────────────────────────────
echo "==> Packing rootfs -> $OUT"
tar -cpzf "$OUT" -C "$ROOTFS" .

echo "==> Verifying archive..."
tar -tvf "$OUT" | grep busybox

# ── Test ──────────────────────────────────────────────────────────────────
echo "==> Testing with tbox..."
"$TBOX_DIR/tbox" run "$OUT" -- /bin/busybox echo "busybox-static works!"

echo ""
echo "Done! Image: $OUT"
echo "Usage:"
echo "  ./tbox run examples/busybox-static.tgz -- echo hello"
echo "  ./tbox run examples/busybox-static.tgz -- sh -c 'ls /bin'"
