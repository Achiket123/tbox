// internal/engine/stop.go
package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/tbox-run/tbox/internal/state"
	"github.com/tbox-run/tbox/internal/termux"
)

// StopContainer sends SIGTERM then SIGKILL to the proot process identified
// by cid or by the human-readable name assigned at run time.
// Safe to call concurrently with RunContainer — no deadlock.
func StopContainer(cidOrName string) error {
	cid, err := resolveCID(cidOrName)
	if err != nil {
		return err
	}

	var prootPID int

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

	if err := safeKill(prootPID, syscall.SIGTERM); err != nil {
		if err == syscall.ESRCH {
			fmt.Fprintf(os.Stderr, "Container %s is already stopped.\n", cid)
			return markStopped(cid)
		}
		return fmt.Errorf("send SIGTERM to PID %d: %w", prootPID, err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !processExists(prootPID) {
			return markStopped(cid)
		}
		time.Sleep(200 * time.Millisecond)
	}

	if err := safeKill(prootPID, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		fmt.Fprintf(os.Stderr, "Warning: SIGKILL failed: %v\n", err)
	}
	time.Sleep(500 * time.Millisecond)

	return markStopped(cid)
}

// resolveCID maps a CID-or-name string to a canonical CID.
// If the input is already a known CID directory it is returned directly.
// Otherwise all container state files are scanned for a matching Name field.
func resolveCID(cidOrName string) (string, error) {
	containersDir := filepath.Join(termux.AppPrivateDir(), "containers")

	// Fast path: the argument is already a valid CID directory.
	if _, err := os.Stat(filepath.Join(containersDir, cidOrName)); err == nil {
		return cidOrName, nil
	}

	// Slow path: scan all containers for a matching name.
	entries, err := os.ReadDir(containersDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("container %q not found", cidOrName)
		}
		return "", fmt.Errorf("read containers dir: %w", err)
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		st, err := state.Read(e.Name())
		if err != nil {
			continue
		}
		if st.Name == cidOrName {
			return e.Name(), nil
		}
	}

	return "", fmt.Errorf("container %q not found (no CID or name match)", cidOrName)
}

func markStopped(cid string) error {
	return state.WithStateLock(cid, func() error {
		st, err := state.Read(cid)
		if err != nil {
			return err
		}
		if st.Status == "exited" || st.Status == "stopped" {
			fmt.Fprintf(os.Stderr, "Container %s already stopped (status: %s).\n", cid, st.Status)
			return nil
		}
		st.Status = "stopped"
		return state.WriteAtomic(cid, st)
	})
}
