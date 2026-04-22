// internal/engine/exec.go
package engine

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/tbox-run/tbox/internal/platform/android"
	"github.com/tbox-run/tbox/internal/state"
	"github.com/tbox-run/tbox/internal/termux"
)

// ExecConfig holds parameters for exec into an existing container.
type ExecConfig struct {
	// CIDOrName is the container ID or human-readable name to exec into.
	CIDOrName string
	// Command is the command and arguments to run inside the container.
	Command []string
	// Env is additional KEY=VAL environment variables to inject.
	Env []string
	// Workdir overrides the working directory; empty means use root /.
	Workdir string
	// Interactive connects stdin to the terminal.
	Interactive bool
}

// ExecContainer runs a command inside the overlay of an already-running
// container. It launches a second proot process against the same overlay
// root — this is correct and safe because proot operates in userspace and
// multiple proot processes can share the same rootfs directory read-only.
//
// The exec process is always foreground: stdin/stdout/stderr are inherited
// from the parent so the caller gets a fully interactive terminal session.
func ExecContainer(cfg ExecConfig) (int, error) {
	cid, err := resolveCID(cfg.CIDOrName)
	if err != nil {
		return -1, err
	}

	st, err := state.Read(cid)
	if err != nil {
		return -1, fmt.Errorf("read container state: %w", err)
	}

	if st.Status != "running" {
		return -1, fmt.Errorf(
			"container %s is not running (status: %s)\n"+
				"Hint: exec only works on running containers; use 'tbox run' to start a new one",
			cid, st.Status)
	}

	// Verify the proot process is still alive before trying to exec.
	if !processExists(st.ProotPID) {
		return -1, fmt.Errorf(
			"container %s proot process (PID %d) is no longer alive",
			cid, st.ProotPID)
	}

	overlayRoot := filepath.Join(termux.AppPrivateDir(), "containers", cid, "overlay")
	if _, err := os.Stat(overlayRoot); os.IsNotExist(err) {
		return -1, fmt.Errorf("overlay for container %s not found at %s", cid, overlayRoot)
	}

	sanitizedEnv := android.GetProotEnv(cfg.Env)

	// Inherit link2symlink state from the existing container run.
	// We check it again here rather than reading from state so this works
	// even if the state file predates the Detached field.
	if prootLink2SymlinkEnabled() {
		l2sDir := filepath.Join(overlayRoot, ".l2s")
		sanitizedEnv = append(sanitizedEnv, "PROOT_L2S_DIR="+l2sDir)
	}

	prootArgs := buildExecArgs(overlayRoot, cfg)

	cmd := exec.Command("proot", prootArgs...) //#nosec G204 — args built from validated inputs
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = sanitizedEnv

	if err := cmd.Start(); err != nil {
		return -1, fmt.Errorf("start exec proot: %w", err)
	}

	waitErr := cmd.Wait()
	if waitErr != nil {
		var exitErr *exec.ExitError
		if isExitError(waitErr, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return -1, waitErr
	}
	return 0, nil
}

// buildExecArgs constructs proot args for an exec session.
// Uses the same overlay root as the container but does NOT re-extract the image.
func buildExecArgs(overlayRoot string, cfg ExecConfig) []string {
	args := []string{"-r", overlayRoot}

	if prootLink2SymlinkEnabled() {
		args = append(args, "--link2symlink")
	}

	selinuxDir := filepath.Join(overlayRoot, "sys/fs/.empty")
	args = append(args, "-b", selinuxDir+":/sys/fs/selinux")

	args = append(args,
		"-b", "/dev:/dev",
		"-b", "/sys:/sys",
		"-b", "/proc:/proc",
	)

	args = android.EnhanceProotArgs(args)

	workdir := cfg.Workdir
	if workdir == "" {
		workdir = "/"
	}
	args = append(args, "-w", workdir)

	for _, e := range cfg.Env {
		args = append(args, "-E", e)
	}

	args = append(args, cfg.Command...)

	return args
}

// isExitError checks whether err is an *exec.ExitError and fills target.
func isExitError(err error, target **exec.ExitError) bool {
	if ee, ok := err.(*exec.ExitError); ok {
		*target = ee
		return true
	}
	return false
}

// RmContainer removes a stopped or exited container's state and overlay.
// Refuses to remove a running container unless force is true.
func RmContainer(cidOrName string, force bool) error {
	cid, err := resolveCID(cidOrName)
	if err != nil {
		return err
	}

	st, err := state.Read(cid)
	if err != nil {
		return fmt.Errorf("read container state: %w", err)
	}

	if st.Status == "running" && !force {
		return fmt.Errorf(
			"container %s is running — stop it first or use --force",
			cid)
	}

	if st.Status == "running" && force {
		// Best-effort stop before removing.
		if stopErr := StopContainer(cid); stopErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: force-stop failed: %v\n", stopErr)
		}
	}

	containerDir := filepath.Join(termux.AppPrivateDir(), "containers", cid)

	// Remove overlay (can be large — do it first so disk is freed even if
	// the state dir removal fails for some reason).
	overlayDir := filepath.Join(containerDir, "overlay")
	if err := os.RemoveAll(overlayDir); err != nil {
		return fmt.Errorf("remove overlay: %w", err)
	}

	// Remove the whole container directory (state, logs, lock).
	if err := os.RemoveAll(containerDir); err != nil {
		return fmt.Errorf("remove container dir: %w", err)
	}

	fmt.Println(cid)
	return nil
}

// resolveNameOrCID is kept in stop.go as resolveCID — imported via same package.
// This comment documents the dependency so it's clear exec.go relies on it.
