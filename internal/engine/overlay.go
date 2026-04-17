// internal/engine/overlay.go
package engine

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tbox-run/tbox/internal/platform/android"
	"github.com/tbox-run/tbox/internal/termux"
)

// PrepareOverlay creates a CoW overlay of the image rootfs for cid.
// Uses hardlinks if supported; falls back to tar-stream copy (fast & Android-safe).
// Returns path to the overlay root (used as proot -r argument).
func PrepareOverlay(imageHash, cid string, cfg Config) (string, error) {
	src := imagePath(imageHash)
	dst := overlayPath(cid)

	// Create container directory structure
	if err := os.MkdirAll(dst, 0755); err != nil {
		return "", fmt.Errorf("create overlay dir: %w", err)
	}

	// Create /tbox virtual API directory (real directory, not bind mount)
	tboxDir := filepath.Join(dst, "tbox")
	if err := os.MkdirAll(tboxDir, 0755); err != nil {
		return "", fmt.Errorf("create /tbox dir: %w", err)
	}

	// Populate /tbox with initial structure
	for _, sub := range []string{"config", "secrets", "ipc", "health"} {
		if err := os.MkdirAll(filepath.Join(tboxDir, sub), 0755); err != nil {
			return "", fmt.Errorf("create /tbox/%s: %w", sub, err)
		}
	}

	// CoW strategy: hardlink tree if filesystem supports it
	if termux.SupportsHardlinks(dst) {
		if err := hardlinkTree(src, dst); err == nil {
			return dst, nil
		}
		// If hardlinks fail, fall through to tar copy
		fmt.Fprintln(os.Stderr, "Warning: hardlinks failed; using tar-stream copy (fast)")
	}

	// Fallback: tar-stream copy (fast, avoids per-file overhead of cp)
	return dst, copyTree(src, dst)
}

// hardlinkTree creates a hardlinked copy of src at dst (CoW base)
func hardlinkTree(src, dst string) error {
	if err := validatePath(src); err != nil {
		return fmt.Errorf("invalid src: %w", err)
	}
	if err := validatePath(dst); err != nil {
		return fmt.Errorf("invalid dst: %w", err)
	}
	// cp -al: archive mode + hardlinks. Takes ~0.1s for 5GB.
	cmd := exec.Command("cp", "-al", src+"/.", dst)
	cmd.Stdout = io.Discard
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// copyTree creates a full recursive copy of src at dst using tar stream.
// This is significantly faster than cp -rP on Android and avoids hardlink restrictions.
func copyTree(src, dst string) error {
	if err := validatePath(src); err != nil {
		return fmt.Errorf("invalid src: %w", err)
	}
	if err := validatePath(dst); err != nil {
		return fmt.Errorf("invalid dst: %w", err)
	}
	// tar -C src -cf - . | tar -C dst -xf -
	// Streams files directly; avoids per-file stat overhead of cp
	cmd := exec.Command("sh", "-c", fmt.Sprintf("tar -C '%s' -cf - . | tar -C '%s' -xf -", src, dst))
	cmd.Stdout = io.Discard
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// validatePath ensures path is under Termux app-private dir
func validatePath(p string) error {
	clean := filepath.Clean(p)
	abs, err := filepath.Abs(clean)
	if err != nil {
		return err
	}
	appDir := termux.AppPrivateDir()
	if !strings.HasPrefix(abs, appDir) {
		return fmt.Errorf("path %s escapes allowed directory %s", abs, appDir)
	}
	return nil
}

// imagePath returns the cached image rootfs path
func imagePath(imageHash string) string {
	return filepath.Join(termux.AppPrivateDir(), "images", imageHash, "rootfs")
}

// overlayPath returns the container overlay path
func overlayPath(cid string) string {
	return filepath.Join(termux.AppPrivateDir(), "containers", cid, "overlay")
}
func prootLink2SymlinkEnabled() bool {
	// Quick test: try proot with --link2symlink on a dummy command.
	// We MUST unset LD_PRELOAD here too, otherwise the check itself may fail!
	cmd := exec.Command("proot", "--link2symlink", "/data/data/com.termux/files/usr/bin/true")
	cmd.Env = android.GetProotEnv(os.Environ())
	out, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[DEBUG] prootLink2SymlinkEnabled failed! error: %v, output: %s\n", err, string(out))
		return false
	}
	return true
}
