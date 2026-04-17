// internal/platform/android/proot.go
package android

import (
	"os"
	"strconv"
	"strings"
)

// EnhanceProotArgs adds Android-specific flags to a proot argument list.
// The caller is responsible for the base "-r", "/dev", "/sys", "/proc" binds.
// This function adds: DNS resolv.conf bind and any future Android quirks.
// Input: base proot args. Output: enhanced args ready for exec.
func EnhanceProotArgs(args []string) []string {
	// L2: /proc is always required (no internal emulation in Termux proot).
	// The caller in run.go now always adds "-b /proc:/proc" unconditionally,
	// so we no longer duplicate it here.

	// CRITICAL: Termux proot extracts a helper loader binary into its default PROOT_TMP_DIR ($PREFIX/tmp),
	// which is then executed *inside* the guest namespace during execve interception.
	// Therefore, this exact directory MUST be bound to the exact same path inside the guest.
	prefixTmp := "/data/data/com.termux/files/usr/tmp"
	args = append(args, "-b", prefixTmp+":"+prefixTmp)

	// Bind DNS resolver from Android's actual location
	args = AddDNSBind(args)

	return args
}

// AddDNSBind appends the resolv.conf bind mount for Android
func AddDNSBind(args []string) []string {
	// Android stores resolv.conf at /system/etc/resolv.conf
	resolvers := []string{
		"/system/etc/resolv.conf",
		"/data/data/com.termux/files/usr/etc/resolv.conf", // Termux-specific
		"/etc/resolv.conf",                                // Fallback for non-Android Linux
	}

	for _, resolver := range resolvers {
		if _, err := os.Stat(resolver); err == nil {
			return append(args, "-b", resolver+":/etc/resolv.conf:ro")
		}
	}

	// If none found, warn but don't fail — container may have its own resolver
	return args
}

// GetAPILevel reads ro.build.version.sdk from /system/build.prop.
// Returns 30 as safe default if unreadable.
func GetAPILevel() int {
	data, err := os.ReadFile("/system/build.prop")
	if err != nil {
		return 30 // safe default for modern Android
	}

	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "ro.build.version.sdk=") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				level, err := strconv.Atoi(strings.TrimSpace(parts[1]))
				if err == nil {
					return level
				}
			}
		}
	}
	return 30
}
