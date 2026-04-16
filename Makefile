# Makefile
.PHONY: all termux-build ci-build ci-build-nocgo clean test

# Default: build for current environment
all: termux-build

# PRIMARY: Build natively inside Termux (CGO enabled, Bionic toolchain)
# This is the RECOMMENDED path — DNS and network calls work correctly
termux-build:
	@echo "→ Building tbox natively in Termux (CGO enabled)..."
	@go build -o tbox ./cmd/tbox
	@echo "✓ Binary: ./tbox"
	@echo "ℹ️  This binary has working DNS via Android's Bionic resolver"

# CI: Cross-compile using Android NDK (requires NDK setup)
# Produces a binary with working DNS, suitable for distribution
ci-build:
ifndef NDK_TOOLCHAIN
	$(error NDK_TOOLCHAIN must be set to Android NDK clang path)
endif
	@echo "→ Cross-compiling with NDK (CGO enabled)..."
	CGO_ENABLED=1 \
	GOOS=android \
	GOARCH=arm64 \
	CC=$(NDK_TOOLCHAIN)/aarch64-linux-android30-clang \
	go build -o tbox-arm64 ./cmd/tbox
	@echo "✓ Binary: ./tbox-arm64"

# CI fallback: No-CGO binary (DNS BROKEN — Phase 1 CLI only)
# Use ONLY if you don't need network calls from the tbox binary itself
ci-build-nocgo:
	@echo "⚠️  WARNING: Building with CGO_ENABLED=0 — DNS will NOT work"
	@echo "   Use only for offline CLI operations in Phase 1"
	CGO_ENABLED=0 GOOS=android GOARCH=arm64 go build -o tbox-arm64-nocgo ./cmd/tbox
	@echo "✓ Binary: ./tbox-arm64-nocgo (NO DNS SUPPORT)"

# Clean build artifacts
clean:
	rm -f tbox tbox-arm64 tbox-arm64-nocgo
	go clean -cache -modcache

# Run acceptance tests (requires Termux + proot installed)
test:
	@echo "→ Running e2e acceptance tests..."
	@test/e2e/basic_run_test.sh
	@test/e2e/concurrent_test.sh
	@echo "✓ All tests passed"
