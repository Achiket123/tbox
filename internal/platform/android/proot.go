// internal/platform/android/proot.go
package android

import (
	"os"
	"strconv"
	"strings"
)

// EnhanceProotArgs adds Android-specific flags to a proot argument list.
// Handles: /proc bind conditional, DNS resolv.conf bind.
// Input: base proot args. Output: enhanced args ready for exec.
func EnhanceProotArgs(args []string) []string {
	api := GetAPILevel()

	// Conditional /proc bind: skip on API ≥ 28 to avoid recursion
	if api < 28 {
		args = append(args, "-b", "/proc:/proc")
	}
	// API 28+: rely on proot's internal /proc emulation

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
		"/etc/resolv.conf", // Fallback for non-Android Linux
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