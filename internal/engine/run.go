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
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/tbox-run/tbox/internal/logs"
	"github.com/tbox-run/tbox/internal/platform/android"
	"github.com/tbox-run/tbox/internal/state"
	"github.com/tbox-run/tbox/internal/termux"
)

// globalProotPID holds the PID of the active proot process for signal forwarding.
// Written atomically by RunContainer; read by GetCurrentPID (called from main).
var globalProotPID int32

// GetCurrentPID returns the PID of the currently running proot process, or 0.
func GetCurrentPID() int32 {
	return atomic.LoadInt32(&globalProotPID)
}

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

	prootTmp := filepath.Join(termux.AppPrivateDir(), "tmp")
	if err := os.MkdirAll(prootTmp, 0700); err != nil {
		return -1, fmt.Errorf("create proot tmp dir: %w", err)
	}
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

	// Phase 3: Setup log files
	stdoutLog, err := logs.OpenLog(cid, "stdout.log")
	if err != nil {
		return -1, fmt.Errorf("open stdout log: %w", err)
	}
	defer stdoutLog.Close()

	stderrLog, err := logs.OpenLog(cid, "stderr.log")
	if err != nil {
		return -1, fmt.Errorf("open stderr log: %w", err)
	}
	defer stderrLog.Close()

	// Phase 5: Build and start proot command.
	// stderrBuf MUST be created before cmd.Start() — reassigning cmd.Stderr
	// after Start() is silently ignored by the OS (B2 fix).
	var stderrBuf bytes.Buffer
	// Finalize environment (P3 fix: unset LD_PRELOAD, add guest PATH)
	sanitizedEnv := android.GetProotEnv(cfg.Env)

	// Set PROOT_L2S_DIR if link2symlink is enabled
	if prootLink2SymlinkEnabled() {
		l2sDir := filepath.Join(overlayPath, ".l2s")
		_ = os.MkdirAll(l2sDir, 0700)
		sanitizedEnv = append(sanitizedEnv, "PROOT_L2S_DIR="+l2sDir)
	}

	prootArgs := buildProotArgs(overlayPath, cfg, sanitizedEnv)

	cmd := exec.Command("proot", prootArgs...)
	cmd.Stdout = io.MultiWriter(stdoutLog, os.Stdout)
	cmd.Stderr = io.MultiWriter(stderrLog, os.Stderr, &stderrBuf)

	// Environment for proot process itself
	cmd.Env = sanitizedEnv

	// Create PROOT_TMP_DIR before executing proot
	tmpDir := filepath.Join(termux.AppPrivateDir(), "tmp")
	_ = os.MkdirAll(tmpDir, 0755)

	// CRITICAL: Termux proot requires the exact loader path to exist inside the guest
	// for the bind-mount to succeed. We must create the identical path inside the overlay.
	overlayTmpDir := filepath.Join(overlayPath, strings.TrimPrefix(tmpDir, "/"))
	_ = os.MkdirAll(overlayTmpDir, 0755)

	// We removed PROOT_TMP_DIR override. Termux proot will default to $PREFIX/tmp.
	// We must ensure the overlay has $PREFIX/tmp created so proot can bind to it.
	prefixTmp := "/data/data/com.termux/files/usr/tmp"
	overlayPrefixTmp := filepath.Join(overlayPath, strings.TrimPrefix(prefixTmp, "/"))
	_ = os.MkdirAll(overlayPrefixTmp, 0755)

	// P3 fix: Hide SELinux to prevent apps/loaders from getting blocked or confused
	selinuxDir := filepath.Join(overlayPath, "sys/fs/.empty")
	_ = os.MkdirAll(selinuxDir, 0700)

	// P3 fix: Termux proot extracts a loader into $PREFIX/tmp and executes it inside the guest.
	// We must ensure this exact path exists in the overlay so the bind-mount succeeds.
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
			StartedAt: time.Now(), // L2 fix: must be set or ps shows year 0001
		})
	}); err != nil {
		// Non-fatal: continue execution, state will be healed on next read
		fmt.Fprintf(os.Stderr, "Warning: could not write initial state: %v\n", err)
	}

	// Phase 6: BLOCKING wait for completion
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
func buildProotArgs(overlayPath string, cfg Config, env []string) []string {
	// Base rootfs bind. We omit -0 here as android.EnhanceProotArgs adds it.
	args := []string{"-r", overlayPath}

	if prootLink2SymlinkEnabled() {
		args = append(args, "--link2symlink")
	}

	// P3 fix: Hide SELinux
	selinuxDir := filepath.Join(overlayPath, "sys/fs/.empty")
	args = append(args, "-b", selinuxDir+":/sys/fs/selinux")

	// System binds (always required by proot)
	args = append(args,
		"-b", "/dev:/dev",
		"-b", "/sys:/sys",
		// L2 fix: /proc must always be bound. Proot has no internal /proc
		// emulation — removing this bind breaks containers on all API levels.
		// The recursion concern is mitigated by PROOT_NO_SECCOMP=1 if needed.
		"-b", "/proc:/proc",
	)

	// L1 fix: delegate Android-specific args (resolv.conf, future platform
	// quirks) to EnhanceProotArgs — the abstraction layer that was bypassed.
	args = android.EnhanceProotArgs(args)

	// User-specified binds (appended last — user has final say on conflicts)
	for _, bind := range cfg.Binds {
		args = append(args, "-b", bind)
	}

	// Working directory
	workdir := cfg.Workdir
	if workdir == "" {
		workdir = "/"
	}
	args = append(args, "-w", workdir)

	args = append(args, cfg.Entrypoint...)

	return args
}

// harmlessPatterns are compiled once at init time for efficiency.
// L4 fix: use regexp.MustCompile — strings.ReplaceAll did literal matching,
// so patterns with `.*` never matched real proot output.
var harmlessPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?m)^.*ptrace: Operation not permitted.*$\n?`),
	regexp.MustCompile(`(?m)^.*seccomp: .* not supported.*$\n?`),
	regexp.MustCompile(`(?m)^.*warning: unable to.*$\n?`),
}

// filterHarmlessPatterns removes expected proot warnings from stderr
func filterHarmlessPatterns(stderr string) string {
	result := stderr
	for _, re := range harmlessPatterns {
		result = re.ReplaceAllString(result, "")
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

// (globalProotPID and GetCurrentPID are declared at the top of this file)

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

		// R4 fix: use containerRunning for live status — state on disk may lag
		// behind reality if the process exited without writing final state.
		displayStatus := st.Status
		if st.Status == "running" && !containerRunning(cid) {
			displayStatus = "exited"
		}

		started := st.StartedAt.Format("2006-01-02 15:04")
		fmt.Printf("%-14s %-20s %-10s %-20s\n", cid, image, displayStatus, started)
	}

	return nil
}

func TailLogs(cid string, follow bool) error {
	return logs.TailLog(cid, follow)
}
