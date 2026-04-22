// internal/state/lock.go
package state

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// WithStateLock acquires an exclusive BSD flock on <cid>/state.lock via
// syscall.Flock, runs fn, then releases. Polls every 100ms up to 5 seconds.
// Using syscall.Flock directly removes the github.com/gofrs/flock dependency.
func WithStateLock(cid string, fn func() error) error {
	dir := containerDir(cid)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create container dir: %w", err)
	}

	lockPath := filepath.Join(dir, "state.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("open lock file: %w", err)
	}
	defer f.Close()

	deadline := time.Now().Add(5 * time.Second)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK {
			return fmt.Errorf("flock: %w", err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("could not acquire state lock for %s (timeout)", cid)
		}
		time.Sleep(100 * time.Millisecond)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck

	return fn()
}
