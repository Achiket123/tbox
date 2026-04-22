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

// globalProotPID holds the PID of the active foreground proot process.
// Written atomically by RunContainer; read by GetCurrentPID in main for
// signal forwarding. Not used in detached mode (process is independent).
var globalProotPID int32

// GetCurrentPID returns the PID of the currently running foreground proot
// process, or 0 if none is active or the container is detached.
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
	Verbose    bool     // Show raw proot stderr (foreground only)
	Detach     bool     // Run container in background, return CID immediately
	Name       string   // Optional human-readable name for the container
}

// RunContainer sets up and executes a container.
// Foreground (Detach=false): blocks until process exits, returns exit code.
// Detached  (Detach=true):   starts process in background, returns 0 + prints CID.
func RunContainer(cfg Config) (int, error) {
	if err := os.MkdirAll(filepath.Join(termux.AppPrivateDir(), "tmp"), 0700); err != nil {
		return -1, fmt.Errorf("create proot tmp dir: %w", err)
	}

	cid, err := state.GenerateCID(cfg.ImagePath)
	if err != nil {
		return -1, fmt.Errorf("generate CID: %w", err)
	}

	imageHash, err := hashFile(cfg.ImagePath)
	if err != nil {
		return -1, fmt.Errorf("hash image: %w", err)
	}

	imageRoot := filepath.Join(termux.AppPrivateDir(), "images", imageHash, "rootfs")
	if _, err := os.Stat(imageRoot); os.IsNotExist(err) {
		if err := os.MkdirAll(imageRoot, 0755); err != nil { //nolint:gosec // G301: rootfs dirs need 0755
			return -1, fmt.Errorf("create image dir: %w", err)
		}
		if err := SafeExtract(cfg.ImagePath, imageRoot); err != nil {
			return -1, fmt.Errorf("extract image: %w", err)
		}
	}

	overlayRoot, err := PrepareOverlay(imageHash, cid, cfg)
	if err != nil {
		return -1, fmt.Errorf("prepare overlay: %w", err)
	}

	sanitizedEnv := android.GetProotEnv(cfg.Env)

	if prootLink2SymlinkEnabled() {
		l2sDir := filepath.Join(overlayRoot, ".l2s")
		_ = os.MkdirAll(l2sDir, 0700)
		sanitizedEnv = append(sanitizedEnv, "PROOT_L2S_DIR="+l2sDir)
	}

	prootArgs := buildProotArgs(overlayRoot, cfg)

	// Ensure proot's loader extraction dirs exist inside the overlay
	if err := ensureProotDirs(overlayRoot); err != nil {
		return -1, err
	}

	if cfg.Detach {
		return 0, runDetached(cid, imageHash, prootArgs, sanitizedEnv, cfg)
	}
	return runForeground(cid, imageHash, overlayRoot, prootArgs, sanitizedEnv, cfg)
}

// ensureProotDirs creates the directories that proot's loader bind-mounts
// need to exist inside the overlay before proot starts.
func ensureProotDirs(overlayRoot string) error {
	tmpDir := filepath.Join(termux.AppPrivateDir(), "tmp")
	_ = os.MkdirAll(tmpDir, 0755) //nolint:gosec // host tmp dir

	overlayTmpDir := filepath.Join(overlayRoot, strings.TrimPrefix(tmpDir, "/"))
	if err := os.MkdirAll(overlayTmpDir, 0755); err != nil { //nolint:gosec // G301: proot needs 0755
		return fmt.Errorf("create overlay tmp dir: %w", err)
	}

	prefixTmp := "/data/data/com.termux/files/usr/tmp"
	overlayPrefixTmp := filepath.Join(overlayRoot, strings.TrimPrefix(prefixTmp, "/"))
	_ = os.MkdirAll(overlayPrefixTmp, 0755) //nolint:gosec // G301: proot loader path

	selinuxDir := filepath.Join(overlayRoot, "sys/fs/.empty")
	_ = os.MkdirAll(selinuxDir, 0700)

	return nil
}

// runForeground runs proot blocking, streaming stdout/stderr to console + log files.
func runForeground(cid, imageHash, overlayRoot string, prootArgs, env []string, cfg Config) (int, error) {
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

	var stderrBuf bytes.Buffer
	cmd := exec.Command("proot", prootArgs...) //#nosec G204 — prootArgs built from validated inputs
	cmd.Stdout = io.MultiWriter(stdoutLog, os.Stdout)
	cmd.Stderr = io.MultiWriter(stderrLog, os.Stderr, &stderrBuf)
	cmd.Env = env

	if err := cmd.Start(); err != nil {
		return -1, fmt.Errorf("start proot: %w", err)
	}

	pid := cmd.Process.Pid
	if pid > math.MaxInt32 || pid < math.MinInt32 {
		return -1, fmt.Errorf("proot PID %d out of int32 range", pid)
	}
	atomic.StoreInt32(&globalProotPID, int32(pid))

	if err := state.WithStateLock(cid, func() error {
		return state.WriteAtomic(cid, state.State{
			CID:       cid,
			Name:      cfg.Name,
			ImageHash: imageHash,
			ProotPID:  pid,
			Status:    "running",
			StartedAt: time.Now(),
			Detached:  false,
		})
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not write initial state: %v\n", err)
	}

	waitErr := cmd.Wait()

	exitCode := 0
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	stderr := stderrBuf.String()
	if exitCode == 0 {
		stderr = filterHarmlessPatterns(stderr)
		if cfg.Verbose && stderr != "" {
			fmt.Fprintf(os.Stderr, "[proot] %s", stderr)
		}
	} else {
		if strings.Contains(stderr, "ptrace") || strings.Contains(stderr, "seccomp") {
			return exitCode, fmt.Errorf(
				"proot platform error: %s\nCheck: Android API level, SELinux policy, battery optimization",
				strings.TrimSpace(stderr))
		}
		if cfg.Verbose {
			fmt.Fprintf(os.Stderr, "[proot stderr] %s", stderr)
		}
	}

	if err := state.WithStateLock(cid, func() error {
		return state.WriteAtomic(cid, state.State{
			CID:       cid,
			Name:      cfg.Name,
			ImageHash: imageHash,
			ProotPID:  pid,
			Status:    "exited",
			ExitCode:  exitCode,
			Detached:  false,
		})
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not write final state: %v\n", err)
	}

	return exitCode, nil
}

// runDetached starts proot as an independent background process and returns
// immediately. The child is fully detached: its stdin is /dev/null, stdout
// and stderr are redirected to log files, and it is placed in its own
// process group so that terminal signals (SIGINT/SIGTERM) do not reach it.
//
// Implementation uses exec.Cmd.SysProcAttr with Setsid=true (creates a new
// session, detaches from the controlling terminal) rather than a shell `&`
// or double-fork, because Go's runtime does not support fork(2) safely.
// Setsid is sufficient on Android/Linux: the child outlives the parent and
// is re-parented to init when the parent exits.
func runDetached(cid, imageHash string, prootArgs, env []string, cfg Config) error {
	stdoutLog, err := logs.OpenLog(cid, "stdout.log")
	if err != nil {
		return fmt.Errorf("open stdout log: %w", err)
	}

	stderrLog, err := logs.OpenLog(cid, "stderr.log")
	if err != nil {
		stdoutLog.Close()
		return fmt.Errorf("open stderr log: %w", err)
	}

	devNull, err := os.Open(os.DevNull)
	if err != nil {
		stdoutLog.Close()
		stderrLog.Close()
		return fmt.Errorf("open /dev/null: %w", err)
	}

	cmd := exec.Command("proot", prootArgs...) //#nosec G204 — prootArgs built from validated inputs
	cmd.Stdin = devNull
	cmd.Stdout = stdoutLog
	cmd.Stderr = stderrLog
	cmd.Env = env

	// Setsid detaches the child from the controlling terminal and places it
	// in a new session. On Linux this is equivalent to the first fork in a
	// classic double-fork daemonise pattern.
	cmd.SysProcAttr = detachSysProcAttr()

	if err := cmd.Start(); err != nil {
		stdoutLog.Close()
		stderrLog.Close()
		devNull.Close()
		return fmt.Errorf("start detached proot: %w", err)
	}

	// Close our copies of the log file descriptors — the child holds its own.
	stdoutLog.Close()
	stderrLog.Close()
	devNull.Close()

	pid := cmd.Process.Pid

	// Write state before returning so 'tbox ps' sees the container immediately.
	if err := state.WithStateLock(cid, func() error {
		return state.WriteAtomic(cid, state.State{
			CID:       cid,
			Name:      cfg.Name,
			ImageHash: imageHash,
			ProotPID:  pid,
			Status:    "running",
			StartedAt: time.Now(),
			Detached:  true,
		})
	}); err != nil {
		// Non-fatal: process is already running; warn and continue.
		fmt.Fprintf(os.Stderr, "Warning: could not write detached state: %v\n", err)
	}

	// Release the child — it now runs independently.
	// cmd.Process.Release() only releases Go-side resources; it does NOT kill
	// the child. This is the correct call after Start() without Wait().
	_ = cmd.Process.Release()

	fmt.Println(cid)
	return nil
}

// buildProotArgs constructs the full proot argument list from the overlay
// root path and container config.
func buildProotArgs(overlayRoot string, cfg Config) []string {
	args := []string{"-r", overlayRoot}

	if prootLink2SymlinkEnabled() {
		args = append(args, "--link2symlink")
	}

	// Hide SELinux filesystem from the guest to avoid loader permission errors
	selinuxDir := filepath.Join(overlayRoot, "sys/fs/.empty")
	args = append(args, "-b", selinuxDir+":/sys/fs/selinux")

	args = append(args,
		"-b", "/dev:/dev",
		"-b", "/sys:/sys",
		"-b", "/proc:/proc",
	)

	// Android-specific: root emulation, kernel release spoof, resolv.conf,
	// and the PROOT_TMP_DIR bind that the Termux proot loader requires.
	args = android.EnhanceProotArgs(args)

	for _, bind := range cfg.Binds {
		args = append(args, "-b", bind)
	}

	workdir := cfg.Workdir
	if workdir == "" {
		workdir = "/"
	}
	args = append(args, "-w", workdir)

	args = append(args, cfg.Entrypoint...)

	return args
}

var harmlessPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?m)^.*ptrace: Operation not permitted.*$\n?`),
	regexp.MustCompile(`(?m)^.*seccomp: .* not supported.*$\n?`),
	regexp.MustCompile(`(?m)^.*warning: unable to.*$\n?`),
}

func filterHarmlessPatterns(stderr string) string {
	result := stderr
	for _, re := range harmlessPatterns {
		result = re.ReplaceAllString(result, "")
	}
	return strings.TrimSpace(result)
}

func generateCID(imagePath string) (string, error) {
	return state.GenerateCID(imagePath)
}

func hashFile(path string) (string, error) {
	return state.HashFile(path)
}

// ListContainers prints a table of all known containers.
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

	fmt.Printf("%-14s %-16s %-20s %-10s %-20s\n",
		"CONTAINER ID", "NAME", "IMAGE", "STATUS", "STARTED")

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		cid := e.Name()

		st, err := state.ReadWithHeal(cid)
		if err != nil {
			continue
		}

		image := st.ImageHash
		if len(image) > 12 {
			image = image[:12]
		}

		displayStatus := st.Status
		if st.Status == "running" && !containerRunning(cid) {
			displayStatus = "exited"
		}
		if st.Detached && st.Status == "running" {
			displayStatus = "running (d)"
		}

		name := st.Name
		if name == "" {
			name = "-"
		}

		started := st.StartedAt.Format("2006-01-02 15:04")
		fmt.Printf("%-14s %-16s %-20s %-10s %-20s\n",
			cid, name, image, displayStatus, started)
	}

	return nil
}

func TailLogs(cid string, follow bool) error {
	return logs.TailLog(cid, follow)
}
