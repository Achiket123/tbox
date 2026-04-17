// internal/platform/android/proot.go
package android

import (
	"os"
	"strconv"
	"strings"
)

// EnhanceProotArgs adds Android-specific flags to a proot argument list.
// The caller is responsible for the base "-r", "/dev", "/sys", "/proc" binds.
// This function adds: root emulation (-0), kernel release, DNS resolv.conf bind,
// and the mandatory guest bind for PROOT_TMP_DIR.
func EnhanceProotArgs(args []string) []string {
	// L2: /proc is always required (no internal emulation in Termux proot).
	// The caller in run.go now always adds "-b /proc:/proc" unconditionally.

	// P3 fix: Use root emulation (-0) to ensure the loader can extract
	// and execute inside the guest namespace without permission issues.
	args = append(args, "-0")

	// P3 fix: Kernel release spoofing allows busybox/musl to see a modern
	// kernel version even if the host is older.
	args = append(args, "--kernel-release=6.17.0-PRoot-Distro")

	// CRITICAL: Termux proot extracts a helper loader binary into its default PROOT_TMP_DIR ($PREFIX/tmp),
	// which is then executed *inside* the guest namespace during execve interception.
	// Therefore, this exact directory MUST be bound to the exact same path inside the guest.
	prefixTmp := "/data/data/com.termux/files/usr/tmp"
	args = append(args, "-b", prefixTmp+":"+prefixTmp)

	// Bind DNS resolver from Android's actual location
	args = AddDNSBind(args)

	return args
}

// GetProotEnv builds the environment variables for proot.
// CRITICAL P3 fix: Unsets LD_PRELOAD to prevent libtermux-exec.so from
// interfering with proot's execve interception.
func GetProotEnv(baseEnv []string) []string {
	var env []string
	prefixTmp := "/data/data/com.termux/files/usr/tmp"

	hasPath := false
	// Preserve user env except LD_PRELOAD
	for _, e := range baseEnv {
		if strings.HasPrefix(e, "LD_PRELOAD=") {
			continue
		}
		if strings.HasPrefix(e, "PATH=") {
			hasPath = true
		}
		env = append(env, e)
	}

	// P3 fix: If no PATH is provided, set a sane default for the guest.
	// Otherwise, it might inherit the host's Termux PATH and fail to find
	// binaries in /bin or /usr/bin.
	if !hasPath {
		env = append(env, "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	}

	// Ensure proot uses the standard Termux tmp path for extraction
	env = append(env, "PROOT_TMP_DIR="+prefixTmp)

	return env
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
