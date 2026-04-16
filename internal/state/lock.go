// internal/state/lock.go
package state

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

// WithStateLock acquires flock on <cid>/state.lock, runs fn, releases.
// Times out after 5 seconds; returns error if lock not acquired.
func WithStateLock(cid string, fn func() error) error {
	lockPath := filepath.Join(containerDir(cid), "state.lock")
	fileLock := flock.New(lockPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	locked, err := fileLock.TryLockContext(ctx, 100*time.Millisecond)
	if err != nil {
		return fmt.Errorf("lock error: %w", err)
	}
	if !locked {
		return fmt.Errorf("could not acquire state lock for %s (timeout)", cid)
	}
	defer fileLock.Unlock()

	return fn()
}
