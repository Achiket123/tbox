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
// Uses hardlinks if supported; falls back to tar-stream copy.
// Returns path to the overlay root (used as proot -r argument).
func PrepareOverlay(imageHash, cid string, cfg Config) (string, error) {
	src := imagePath(imageHash)
	dst := overlayPath(cid)

	if err := os.MkdirAll(dst, 0755); err != nil { //nolint:gosec // G301
		return "", fmt.Errorf("create overlay dir: %w", err)
	}

	tboxDir := filepath.Join(dst, "tbox")
	if err := os.MkdirAll(tboxDir, 0755); err != nil { //nolint:gosec // G301
		return "", fmt.Errorf("create /tbox dir: %w", err)
	}

	for _, sub := range []string{"config", "ipc", "health"} {
		if err := os.MkdirAll(filepath.Join(tboxDir, sub), 0755); err != nil { //nolint:gosec // G301
			return "", fmt.Errorf("create /tbox/%s: %w", sub, err)
		}
	}
	// secrets gets tighter permissions
	if err := os.MkdirAll(filepath.Join(tboxDir, "secrets"), 0700); err != nil {
		return "", fmt.Errorf("create /tbox/secrets: %w", err)
	}

	if termux.SupportsHardlinks(dst) {
		if err := hardlinkTree(src, dst); err == nil {
			return dst, nil
		}
		fmt.Fprintln(os.Stderr, "Warning: hardlinks failed; using tar-stream copy")
	}

	return dst, copyTree(src, dst)
}

func hardlinkTree(src, dst string) error {
	if err := validatePath(src); err != nil {
		return fmt.Errorf("invalid src: %w", err)
	}
	if err := validatePath(dst); err != nil {
		return fmt.Errorf("invalid dst: %w", err)
	}
	cmd := exec.Command("cp", "-al", src+"/.", dst) //#nosec G204 — paths validated above
	cmd.Stdout = io.Discard
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func copyTree(src, dst string) error {
	if err := validatePath(src); err != nil {
		return fmt.Errorf("invalid src: %w", err)
	}
	if err := validatePath(dst); err != nil {
		return fmt.Errorf("invalid dst: %w", err)
	}
	// tar stream avoids per-file stat overhead of cp -rP and works on all Android FSes
	cmd := exec.Command("sh", "-c",
		"tar -C '\"$src\"' -cf - . | tar -C '\"$dst\"' -xf -") //#nosec G204
	cmd.Env = append(os.Environ(), "src="+src, "dst="+dst)
	cmd.Stdout = io.Discard
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

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

// prootLink2SymlinkEnabled checks if proot --link2symlink works on this device.
// Called from run.go; defined here to keep overlay-related helpers together.
func prootLink2SymlinkEnabled() bool {
	cmd := exec.Command("proot", "--link2symlink", "/data/data/com.termux/files/usr/bin/true")
	cmd.Env = android.GetProotEnv(os.Environ())
	err := cmd.Run()
	return err == nil
}

func imagePath(imageHash string) string {
	return filepath.Join(termux.AppPrivateDir(), "images", imageHash, "rootfs")
}

func overlayPath(cid string) string {
	return filepath.Join(termux.AppPrivateDir(), "containers", cid, "overlay")
}
