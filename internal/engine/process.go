// internal/engine/process.go
package engine

import (
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/tbox-run/tbox/internal/state"
)

// processExists checks if a PID is currently alive
func processExists(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// safeKill verifies the process is actually proot before sending signal
// Mitigates PID recycling risk on Android
func safeKill(pid int, sig syscall.Signal) error {
	// First: basic existence check
	if err := syscall.Kill(pid, 0); err != nil {
		return err
	}

	// Second: verify comm name (Android /proc is readable for own UID)
	// #nosec G304 — /proc is system directory; pid verified by processExists()
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		// SELinux may block read; fall back to PID-only (documented risk)
		return syscall.Kill(pid, sig)
	}

	comm := strings.TrimSpace(string(data))
	// Accept proot binary names across architectures
	if comm != "proot" && !strings.HasPrefix(comm, "proot-") {
		return fmt.Errorf(
			"PID %d is '%s', not proot — refusing to signal (PID recycling?)",
			pid, comm)
	}

	return syscall.Kill(pid, sig)
}

// containerRunning checks if the container's proot process is still alive
func containerRunning(cid string) bool {
	st, err := state.Read(cid)
	if err != nil {
		return false
	}
	if st.Status != "running" {
		return false
	}
	return processExists(st.ProotPID)
}
