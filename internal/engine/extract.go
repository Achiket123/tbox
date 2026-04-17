// internal/engine/extract.go
package engine

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// SafeExtract extracts a .tgz to dst, rejecting any Zip Slip paths.
// Returns error with path if any entry would escape dst.
func SafeExtract(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	absRoot, err := filepath.Abs(dst)
	if err != nil {
		return fmt.Errorf("resolve target: %w", err)
	}

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}

		// Resolve the final path and ensure it stays inside dst
		target := filepath.Join(absRoot, hdr.Name)
		clean := filepath.Clean(target)
		if !strings.HasPrefix(clean, absRoot+string(os.PathSeparator)) && clean != absRoot {
			return fmt.Errorf("unsafe archive: path escapes container: %s", hdr.Name)
		}

		// SECURITY: Reject absolute symlink targets that point outside
		if hdr.Typeflag == tar.TypeSymlink {
			link := hdr.Linkname
			if filepath.IsAbs(link) {
				// Resolve symlink target relative to container root
				linkTarget := filepath.Join(absRoot, link)
				linkClean := filepath.Clean(linkTarget)
				if !strings.HasPrefix(linkClean, absRoot+string(os.PathSeparator)) && linkClean != absRoot {
					return fmt.Errorf("unsafe archive: symlink escapes container: %s -> %s", hdr.Name, link)
				}
			}
		}

		// SECURITY S1: Hardlinks use Linkname as a relative path within the
		// archive. Apply the same escape check as for regular paths.
		if hdr.Typeflag == tar.TypeLink {
			linkTarget := filepath.Join(absRoot, hdr.Linkname)
			linkClean := filepath.Clean(linkTarget)
			if !strings.HasPrefix(linkClean, absRoot+string(os.PathSeparator)) && linkClean != absRoot {
				return fmt.Errorf("unsafe archive: hardlink escapes container: %s -> %s", hdr.Name, hdr.Linkname)
			}
		}

		if err := extractEntry(tr, hdr, clean, absRoot); err != nil {
			return fmt.Errorf("extract %s: %w", hdr.Name, err)
		}
	}

	return nil
}

// extractEntry extracts a single tar entry to the target path.
// absRoot is the container root used to resolve hardlink targets consistently
// with the escape checks performed in SafeExtract.
func extractEntry(tr *tar.Reader, hdr *tar.Header, target, absRoot string) error {
	switch hdr.Typeflag {
	case tar.TypeDir:
		// Mask to 12 permission bits; & prevents int64→uint32 overflow for
		// malformed headers. #nosec G115 — masked value always fits in uint32.
		mode := os.FileMode(hdr.Mode&0o7777) | 0111 // #nosec G115
		return os.MkdirAll(target, mode)            //nolint:gosec // G301: container dirs need execute bits

	case tar.TypeReg:
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil { //nolint:gosec // G301: container dirs need 0755 execute bits
			return err
		}
		// The gosec G115 warning is a false positive here — hdr.Mode is
		// already a valid file mode value from the tar header.
		mode := os.FileMode(hdr.Mode) // #nosec G115
		f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(f, tr)
		return err

	case tar.TypeSymlink:
		// Create parent dirs if needed
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil { //nolint:gosec // G301: container dirs need 0755 execute bits
			return err
		}
		return os.Symlink(hdr.Linkname, target)

	case tar.TypeLink:
		// R1 fix: resolve hardlink target against absRoot, not filepath.Dir(target).
		// filepath.Dir(target) gives the destination parent, which is a different
		// base than the escape check above used — causing an inconsistency that
		// could let a crafted Linkname bypass the check.
		linkTarget := filepath.Join(absRoot, hdr.Linkname)
		return os.Link(linkTarget, target)

	default:
		// Skip unsupported types (devices, sockets, etc.) — not needed for CLI tools
		return nil
	}
}
