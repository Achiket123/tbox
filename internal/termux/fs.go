// internal/termux/fs.go
package termux

import (
	"fmt"
	"os"
	"path/filepath"
)

// AppPrivateDir returns the Termux-sandboxed base directory for tbox data.
// Guaranteed to be on an ext4/f2fs filesystem that supports flock.
func AppPrivateDir() string {
	// Termux home: /data/data/com.termux/files/home
	// We store tbox data in a subdirectory
	home := os.Getenv("HOME")
	if home == "" {
		home = "/data/data/com.termux/files/home"
	}
	return filepath.Join(home, ".tbox")
}

// SupportsHardlinks probes whether dir's filesystem supports hardlinks.
// Creates and immediately removes two probe files; returns false on any error.
func SupportsHardlinks(dir string) bool {
	// Create a temporary probe file
	probe, err := os.CreateTemp(dir, ".tbox_hl_probe_*")
	if err != nil {
		return false
	}
	probePath := probe.Name()

	if err := probe.Close(); err != nil {
		// Log but don't fail — probe cleanup is best-effort
		fmt.Fprintf(os.Stderr, "Warning: failed to close probe file: %v\n", err)
	}
	defer os.Remove(probePath)

	// Try to create a hardlink to it
	linkPath := probePath + ".link"
	if err := os.Link(probePath, linkPath); err != nil {
		return false
	}
	defer os.Remove(linkPath)

	return true
}
