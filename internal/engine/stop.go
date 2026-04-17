// internal/engine/stop.go
package engine

import (
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/tbox-run/tbox/internal/state"
)

// StopContainer sends SIGTERM then SIGKILL to the proot process.
// Safe to call concurrently with RunContainer — no deadlock.
func StopContainer(cid string) error {
	var prootPID int

	// Phase 1: Read PID under lock, then RELEASE immediately
	if err := state.WithStateLock(cid, func() error {
		st, err := state.Read(cid)
		if err != nil {
			return err
		}
		if st.Status != "running" {
			return fmt.Errorf("container %s is not running (status: %s)", cid, st.Status)
		}
		prootPID = st.ProotPID
		return nil
	}); err != nil {
		return err
	}

	if prootPID == 0 {
		return fmt.Errorf("container %s has no recorded proot PID", cid)
	}

	// Phase 2: Send signals and poll WITHOUT holding any lock
	// This prevents deadlock with RunContainer's final state write

	// SIGTERM first (graceful shutdown)
	if err := safeKill(prootPID, syscall.SIGTERM); err != nil {
		if err == syscall.ESRCH {
			// Already dead — notify user rather than returning silently
			fmt.Fprintf(os.Stderr, "Container %s is already stopped.\n", cid)
			return markStopped(cid)
		}
		return fmt.Errorf("send SIGTERM to PID %d: %w", prootPID, err)
	}

	// Poll for termination (10s timeout, 200ms intervals)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !processExists(prootPID) {
			return markStopped(cid)
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Phase 3: SIGKILL fallback
	if err := safeKill(prootPID, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		// FIX: Use os.Stderr instead of undefined stderrLog
		fmt.Fprintf(os.Stderr, "Warning: SIGKILL failed: %v\n", err)
	}
	time.Sleep(500 * time.Millisecond) // Let kernel clean up

	return markStopped(cid)
}

// markStopped updates state to 'stopped' ONLY if not already 'exited'
// This preserves exit codes from natural termination
func markStopped(cid string) error {
	return state.WithStateLock(cid, func() error {
		st, err := state.Read(cid)
		if err != nil {
			return err
		}
		// CRITICAL: If RunContainer already wrote 'exited', preserve it
		if st.Status == "exited" || st.Status == "stopped" {
			fmt.Fprintf(os.Stderr, "Container %s is already stopped (status: %s).\n", cid, st.Status)
			return nil // natural exit wins; don't overwrite exit code
		}
		st.Status = "stopped"
		return state.WriteAtomic(cid, st)
	})
}
