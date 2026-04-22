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
// Log directory is created with 0700; log files with 0600.
func OpenLog(cid, name string) (*os.File, error) {
	logDir := filepath.Join(termux.AppPrivateDir(), "containers", cid, "logs")
	if err := os.MkdirAll(logDir, 0700); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	path := filepath.Join(logDir, name)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("open log %s: %w", name, err)
	}
	return f, nil
}

// TailLog prints container logs to stdout.
// If follow is true, polls for new content every 100ms until Ctrl-C.
func TailLog(cid string, follow bool) error {
	path := logPath(cid, "stdout.log")
	f, err := os.Open(path) //#nosec G304 — path constructed from validated CID
	if err != nil {
		return fmt.Errorf("no logs for %s: %w", cid, err)
	}
	defer f.Close()

	var pos int64
	buf := make([]byte, 4096)

	for {
		if _, err := f.Seek(pos, io.SeekStart); err != nil {
			return fmt.Errorf("seek: %w", err)
		}

		n, err := f.Read(buf)
		if n > 0 {
			_, _ = os.Stdout.Write(buf[:n])
			pos += int64(n)
		}

		if !follow {
			break
		}

		if err == io.EOF {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		if err != nil {
			return fmt.Errorf("read log: %w", err)
		}
	}

	return nil
}

func logPath(cid, name string) string {
	return filepath.Join(termux.AppPrivateDir(), "containers", cid, "logs", name)
}
