// internal/logs/tail.go
package logs

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/tbox-run/tbox/internal/termux"
)

// OpenLog opens (or creates) a log file for a container.
// Returns (*os.File, error) — callers must check error before use.
func OpenLog(cid, name string) (*os.File, error) {
	logDir := filepath.Join(termux.AppPrivateDir(), "containers", cid, "logs")
	if err := os.MkdirAll(logDir, 0755); err != nil { //nolint:gosec // G301: 0755 needed for container log dirs
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	path := filepath.Join(logDir, name)
	// Open with O_APPEND for concurrent writes; O_CREATE if missing
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("open log %s: %w", name, err)
	}
	return f, nil
}

// TailLog prints container logs to stdout.
// If follow=true, polls for new content.
// Uses position-tracking Seek — safe for concurrent writers.
// Note: Does not auto-detect container stop; use Ctrl-C to exit follow mode.
func TailLog(cid string, follow bool) error {
	path := logPath(cid, "stdout.log")
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("no logs for %s: %w", cid, err)
	}
	defer f.Close()

	var pos int64 // tracks how far we've read
	buf := make([]byte, 4096)

	for {
		// Always seek to last known position before reading
		if _, err := f.Seek(pos, io.SeekStart); err != nil {
			return fmt.Errorf("seek: %w", err)
		}

		n, err := f.Read(buf)
		if n > 0 {
			_, _ = os.Stdout.Write(buf[:n]) // ignore write errors, continue reading
			pos += int64(n)
		}

		if !follow {
			break // non-follow: stop at first EOF
		}

		if err == io.EOF {
			// Poll interval: 100ms (balance responsiveness vs CPU)
			// User presses Ctrl-C to exit follow mode
			time.Sleep(100 * time.Millisecond)
			continue
		}

		if err != nil {
			return fmt.Errorf("read log: %w", err)
		}
	}

	return nil
}

// logPath returns the path to a container's log file
func logPath(cid, name string) string {
	return filepath.Join(termux.AppPrivateDir(), "containers", cid, "logs", name)
}
