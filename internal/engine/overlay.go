// internal/engine/overlay.go
package engine

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tbox-run/tbox/internal/termux"
)

// PrepareOverlay creates a CoW overlay of the image rootfs for cid.
// Uses hardlinks if supported; falls back to full copy with warning.
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
	if termux.SupportsHardlinks(dst) && prootLink2SymlinkEnabled() {
		return dst, hardlinkTree(src, dst)
	}

	// Fallback: full copy (slower, but works on all filesystems)
	fmt.Fprintln(os.Stderr,
		"Warning: hardlinks or proot --link2symlink unavailable; using full copy (slower)")
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
	cmd := exec.Command("cp", "-al", src+"/.", dst)
	// Use cp -al: archive mode + hardlinks
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func validatePath(p string) error {
	clean := filepath.Clean(p)
	abs, err := filepath.Abs(clean)
	if err != nil {
		return err
	}
	// Ensure path is under Termux app-private dir
	appDir := termux.AppPrivateDir()
	if !strings.HasPrefix(abs, appDir) {
		return fmt.Errorf("path %s escapes allowed directory %s", abs, appDir)
	}
	return nil
}

// copyTree creates a full recursive copy of src at dst
func copyTree(src, dst string) error {
	if err := validatePath(src); err != nil {
		return fmt.Errorf("invalid src: %w", err)
	}
	if err := validatePath(dst); err != nil {
		return fmt.Errorf("invalid dst: %w", err)
	}
	cmd := exec.Command("cp", "-rP", src+"/.", dst)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// prootLink2SymlinkEnabled checks if proot's --link2symlink works
// (may be blocked by SELinux on some Android devices)
func prootLink2SymlinkEnabled() bool {
	// Quick test: try proot with --link2symlink on a dummy command
	cmd := exec.Command("proot", "--link2symlink", "-r", "/", "true")
	return cmd.Run() == nil
}

// imagePath returns the cached image rootfs path
func imagePath(imageHash string) string {
	return filepath.Join(termux.AppPrivateDir(), "images", imageHash, "rootfs")
}

// overlayPath returns the container overlay path
func overlayPath(cid string) string {
	return filepath.Join(termux.AppPrivateDir(), "containers", cid, "overlay")
}
