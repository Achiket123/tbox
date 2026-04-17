// internal/state/state.go
package state

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// State represents a container's lifecycle state
type State struct {
	CID       string    `json:"cid"`
	ImageHash string    `json:"image_hash"`
	ProotPID  int       `json:"proot_pid"`
	StartedAt time.Time `json:"started_at"`
	Status    string    `json:"status"` // running, exited, stopped
	ExitCode  int       `json:"exit_code,omitempty"`
}

// Read loads container state from disk.
// Returns (State{}, error) — callers MUST check error before using State.
func Read(cid string) (State, error) {
	path := statePath(cid)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return State{}, fmt.Errorf("container %s not found", cid)
		}
		return State{}, fmt.Errorf("read state %s: %w", cid, err)
	}

	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return State{}, fmt.Errorf("corrupt state %s: %w", cid, err)
	}
	return st, nil
}

// ReadWithHeal loads state and auto-heals stale 'running' if PID is gone
func ReadWithHeal(cid string) (State, error) {
	st, err := Read(cid)
	if err != nil {
		return State{}, err
	}

	// Auto-heal: if status is 'running' but PID is dead, mark as crashed
	if st.Status == "running" && !processExists(st.ProotPID) {
		st.Status = "exited"
		st.ExitCode = -1 // crashed / unknown
		// Best-effort write; ignore error (read-only path may fail)
		_ = WithStateLock(cid, func() error {
			return WriteAtomic(cid, st)
		})
	}
	return st, nil
}

// WriteAtomic persists state atomically via temp+fsync+rename.
// Caller MUST hold per-container lock (use WithStateLock).
func WriteAtomic(cid string, st State) error {
	dir := containerDir(cid)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}

	// Write to temp file first
	tmp, err := os.CreateTemp(dir, "state-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // cleanup on failure

	// Marshal with indentation for readability
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil { // fsync!
		return fmt.Errorf("fsync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}

	// Atomic rename — POSIX guarantees this is atomic
	return os.Rename(tmpPath, statePath(cid))
}

// GenerateCID creates a short, unique container ID from image path + randomness.
// Uses crypto/rand so IDs are globally unique even for rapid concurrent runs.
func GenerateCID(imagePath string) (string, error) {
	h := sha256.New()
	h.Write([]byte(imagePath))
	h.Write([]byte(time.Now().Format(time.RFC3339Nano)))
	rndBytes := make([]byte, 8)
	if _, err := rand.Read(rndBytes); err != nil {
		return "", fmt.Errorf("generate random bytes: %w", err)
	}
	h.Write(rndBytes)
	return hex.EncodeToString(h.Sum(nil))[:12], nil
}

// HashFile computes SHA256 of a file (for content-addressable caching)
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// statePath returns the path to a container's state.json
func statePath(cid string) string {
	return filepath.Join(containerDir(cid), "state.json")
}

// containerDir returns the directory for a container's state files
func containerDir(cid string) string {
	// Inline AppPrivateDir to avoid import cycle with termux package
	home := os.Getenv("HOME")
	if home == "" {
		home = "/data/data/com.termux/files/home"
	}
	return filepath.Join(home, ".tbox", "containers", cid)
}

// processExists checks if a PID is currently alive (duplicated to avoid cycle)
func processExists(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
