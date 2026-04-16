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

		if err := extractEntry(tr, hdr, clean); err != nil {
			return fmt.Errorf("extract %s: %w", hdr.Name, err)
		}
	}

	return nil
}

// extractEntry extracts a single tar entry to the target path
func extractEntry(tr *tar.Reader, hdr *tar.Header, target string) error {
	switch hdr.Typeflag {
	case tar.TypeDir:
		mode := os.FileMode(hdr.Mode & 0755)
		if hdr.Typeflag == tar.TypeDir {
			mode |= 0111 // ensure execute bit for directories
		}
		return os.MkdirAll(target, mode)

	case tar.TypeReg:
		// Create parent dirs if needed
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}

		mode := os.FileMode(hdr.Mode & 0644)
		f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(f, tr)
		return err

	case tar.TypeSymlink:
		// Create parent dirs if needed
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		return os.Symlink(hdr.Linkname, target)

	case tar.TypeLink:
		linkTarget := filepath.Join(filepath.Dir(target), hdr.Linkname)
		return os.Link(linkTarget, target)

	default:
		// Skip unsupported types (devices, sockets, etc.) — not needed for CLI tools
		return nil
	}
}
