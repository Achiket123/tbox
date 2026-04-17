// internal/engine/run.go
package engine

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/tbox-run/tbox/internal/logs"
	"github.com/tbox-run/tbox/internal/platform/android"
	"github.com/tbox-run/tbox/internal/state"
	"github.com/tbox-run/tbox/internal/termux"
)

// Config holds container execution parameters
type Config struct {
	ImagePath  string   // Path to .tgz image file
	Entrypoint []string // Command to execute inside container
	Env        []string // Environment variables (KEY=VAL)
	Workdir    string   // Working directory inside container
	Binds      []string // Bind mounts: host:container[:ro]
	Verbose    bool     // Show raw proot stderr
}

// RunContainer executes cmd inside container using proot.
// Blocks until the process exits. Returns exit code and any error.
func RunContainer(cfg Config) (int, error) {
	// Generate container ID (content-addressable by image + timestamp)
	cid, err := generateCID(cfg.ImagePath)
	if err != nil {
		return -1, fmt.Errorf("generate CID: %w", err)
	}

	// Phase 1: Extract image to cache (content-addressable by tarball hash)
	imageHash, err := hashFile(cfg.ImagePath)
	if err != nil {
		return -1, fmt.Errorf("hash image: %w", err)
	}
	imageRoot := filepath.Join(termux.AppPrivateDir(), "images", imageHash, "rootfs")

	// Check cache; extract if missing
	if _, err := os.Stat(imageRoot); os.IsNotExist(err) {
		if err := os.MkdirAll(imageRoot, 0755); err != nil {
			return -1, fmt.Errorf("create image dir: %w", err)
		}
		if err := SafeExtract(cfg.ImagePath, imageRoot); err != nil {
			return -1, fmt.Errorf("extract image: %w", err)
		}
	}

	// Phase 2: Prepare CoW overlay for this container
	overlayPath, err := PrepareOverlay(imageHash, cid, cfg)
	if err != nil {
		return -1, fmt.Errorf("prepare overlay: %w", err)
	}

	// Phase 3: Build proot arguments
	prootArgs := buildProotArgs(overlayPath, cfg)

	// Phase 4: Setup log files
	stdoutLog := logs.OpenLog(cid, "stdout.log")
	stderrLog := logs.OpenLog(cid, "stderr.log")
	defer stdoutLog.Close()
	defer stderrLog.Close()

	// Phase 5: Build and start proot command
	cmd := exec.Command("proot", prootArgs...)
	cmd.Stdout = io.MultiWriter(stdoutLog, os.Stdout)
	cmd.Stderr = io.MultiWriter(stderrLog, os.Stderr, &bytes.Buffer{}) // capture for filtering
	if err := cmd.Start(); err != nil {
		return -1, fmt.Errorf("start proot: %w", err)
	}

	// Store PID atomically for signal forwarding
	pid := cmd.Process.Pid
	if pid > math.MaxInt32 || pid < math.MinInt32 {
		return -1, fmt.Errorf("proot PID %d out of range for atomic storage", pid)
	}
	atomic.StoreInt32(&globalProotPID, int32(pid))

	// Write initial state under lock
	if err := state.WithStateLock(cid, func() error {
		return state.WriteAtomic(cid, state.State{
			CID:       cid,
			ImageHash: imageHash,
			ProotPID:  cmd.Process.Pid,
			Status:    "running",
		})
	}); err != nil {
		// Non-fatal: continue execution, state will be healed on next read
		fmt.Fprintf(os.Stderr, "Warning: could not write initial state: %v\n", err)
	}

	// Phase 6: BLOCKING wait for completion
	var stderrBuf bytes.Buffer
	cmd.Stderr = io.MultiWriter(stderrLog, &stderrBuf)
	err = cmd.Wait()

	// Determine exit code
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	// Phase 7: Filter stderr and write final state
	stderr := stderrBuf.String()
	if exitCode == 0 {
		// Only filter harmless patterns on success
		stderr = filterHarmlessPatterns(stderr)
		if cfg.Verbose && stderr != "" {
			fmt.Fprintf(os.Stderr, "[proot] %s", stderr)
		}
	} else {
		// On failure, provide contextual error messages
		if strings.Contains(stderr, "ptrace") || strings.Contains(stderr, "seccomp") {
			return exitCode, fmt.Errorf(
				"proot platform error: %s\n→ Check: Android API level, SELinux policy, battery optimization",
				strings.TrimSpace(stderr))
		}
		if cfg.Verbose {
			fmt.Fprintf(os.Stderr, "[proot stderr] %s", stderr)
		}
	}

	// Write final state under lock
	if err := state.WithStateLock(cid, func() error {
		return state.WriteAtomic(cid, state.State{
			CID:       cid,
			ImageHash: imageHash,
			ProotPID:  cmd.Process.Pid,
			Status:    "exited",
			ExitCode:  exitCode,
		})
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not write final state: %v\n", err)
	}

	return exitCode, nil
}

// buildProotArgs constructs the proot command line
func buildProotArgs(overlayPath string, cfg Config) []string {
	args := []string{"-r", overlayPath}

	// System binds (always)
	args = append(args,
		"-b", "/dev:/dev",
		"-b", "/sys:/sys",
	)

	// Conditional /proc bind based on Android API level
	if android.GetAPILevel() < 28 {
		args = append(args, "-b", "/proc:/proc")
	}
	// API 28+: rely on proot's internal /proc emulation (avoids recursion)

	// DNS resolver bind (Android-specific)
	args = android.AddDNSBind(args)

	// User-specified binds (appended last — user has final say on conflicts)
	for _, bind := range cfg.Binds {
		args = append(args, "-b", bind)
	}

	// Working directory
	if cfg.Workdir != "" && cfg.Workdir != "/" {
		args = append(args, "-w", cfg.Workdir)
	}

	// Environment variables
	for _, env := range cfg.Env {
		args = append(args, "-E", env)
	}

	// proot behavior flags
	args = append(args,
		"--kill-on-exit", // Ensure children die with proot
		// "--quiet",        // Reduce noise (we handle errors ourselves)
	)

	// Entry point command
	// args = append(args, "--")
	args = append(args, cfg.Entrypoint...)

	return args
}

// filterHarmlessPatterns removes expected proot warnings from stderr
func filterHarmlessPatterns(stderr string) string {
	// Known harmless patterns on Android
	patterns := []string{
		"ptrace: Operation not permitted",
		"seccomp: .* not supported",
		"warning: unable to.*",
	}
	result := stderr
	for _, pat := range patterns {
		// Simple substring removal (Phase 1; regex in Phase 2)
		result = strings.ReplaceAll(result, pat, "")
	}
	return strings.TrimSpace(result)
}

// generateCID creates a short, unique container ID
func generateCID(imagePath string) (string, error) {
	// Simple: hash(imagePath + timestamp + random)
	// Phase 1: use first 12 hex chars of SHA256
	return state.GenerateCID(imagePath)
}

// hashFile computes SHA256 of a file (for content-addressable caching)
func hashFile(path string) (string, error) {
	return state.HashFile(path)
}

// Global PID reference for signal forwarding (set by RunContainer)
var globalProotPID int32

// ListContainers prints a table of all known containers.
// Reads state files without acquiring locks (relies on atomic rename).
func ListContainers() error {
	containersDir := filepath.Join(termux.AppPrivateDir(), "containers")

	entries, err := os.ReadDir(containersDir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No containers found")
			return nil
		}
		return fmt.Errorf("read containers dir: %w", err)
	}

	if len(entries) == 0 {
		fmt.Println("No containers found")
		return nil
	}

	// Print header
	fmt.Printf("%-14s %-20s %-10s %-20s\n", "CONTAINER ID", "IMAGE", "STATUS", "STARTED")

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		cid := e.Name()

		// Read state without lock (relies on atomic rename guarantee)
		st, err := state.ReadWithHeal(cid)
		if err != nil {
			continue // skip unreadable states
		}

		// Truncate image hash for display
		image := st.ImageHash
		if len(image) > 12 {
			image = image[:12]
		}

		started := st.StartedAt.Format("2006-01-02 15:04")
		fmt.Printf("%-14s %-20s %-10s %-20s\n", cid, image, st.Status, started)
	}

	return nil
}

func TailLogs(cid string, follow bool) error {
	return logs.TailLog(cid, follow)
}
