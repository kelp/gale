package download

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

// ExtractTarGz extracts a tar.gz file to destDir, preserving
// relative paths and creating directories as needed.
func ExtractTarGz(archivePath, destDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("create gzip reader: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	if err := extractTar(tr, destDir); err != nil {
		return err
	}

	return nil
}

// ExtractZip extracts a zip file to destDir, preserving
// relative paths and creating directories as needed.
func ExtractZip(archivePath, destDir string) error {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open zip archive: %w", err)
	}
	defer r.Close()

	for _, zf := range r.File {
		target := filepath.Join(destDir, zf.Name) //nolint:gosec // G305 — path validated below
		cleanDest := filepath.Clean(destDir) + string(os.PathSeparator)

		if !strings.HasPrefix(filepath.Clean(target), cleanDest) {
			return fmt.Errorf("illegal path in archive: %s", zf.Name)
		}

		if zf.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("create directory %s: %w",
					zf.Name, err)
			}
			continue
		}

		if err := os.MkdirAll(
			filepath.Dir(target), 0o755,
		); err != nil {
			return fmt.Errorf(
				"create parent directory for %s: %w",
				zf.Name, err,
			)
		}

		rc, err := zf.Open()
		if err != nil {
			return fmt.Errorf("open zip entry %s: %w",
				zf.Name, err)
		}

		if err := writeFile(target, rc, zf.Mode()); err != nil {
			rc.Close()
			return fmt.Errorf("extract %s: %w", zf.Name, err)
		}
		rc.Close()
	}

	return nil
}

// ExtractTarZstd extracts a tar.zst file to destDir, preserving
// relative paths and creating directories as needed.
func ExtractTarZstd(archivePath, destDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()

	zr, err := zstd.NewReader(f)
	if err != nil {
		return fmt.Errorf("create zstd reader: %w", err)
	}
	defer zr.Close()

	tr := tar.NewReader(zr)
	if err := extractTar(tr, destDir); err != nil {
		return err
	}

	return nil
}

// ExtractTarXz extracts a tar.xz file to destDir, preserving
// relative paths and creating directories as needed.
func ExtractTarXz(archivePath, destDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()

	xr, err := xz.NewReader(f)
	if err != nil {
		return fmt.Errorf("create xz reader: %w", err)
	}

	tr := tar.NewReader(xr)
	return extractTar(tr, destDir)
}

// ExtractTarBz2 extracts a tar.bz2 file to destDir, preserving
// relative paths and creating directories as needed.
func ExtractTarBz2(archivePath, destDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()

	br := bzip2.NewReader(f)
	tr := tar.NewReader(br)
	return extractTar(tr, destDir)
}

// ExtractSource extracts a source archive to destDir,
// detecting the format from the file extension.
func ExtractSource(archivePath, destDir string) error {
	switch {
	case strings.HasSuffix(archivePath, ".tar.gz"),
		strings.HasSuffix(archivePath, ".tgz"):
		return ExtractTarGz(archivePath, destDir)
	case strings.HasSuffix(archivePath, ".tar.xz"):
		return ExtractTarXz(archivePath, destDir)
	case strings.HasSuffix(archivePath, ".tar.bz2"):
		return ExtractTarBz2(archivePath, destDir)
	case strings.HasSuffix(archivePath, ".tar.zst"):
		return ExtractTarZstd(archivePath, destDir)
	case strings.HasSuffix(archivePath, ".zip"):
		return ExtractZip(archivePath, destDir)
	default:
		return fmt.Errorf(
			"unsupported archive format: %s", archivePath,
		)
	}
}

// ensureNoSymlinkParent verifies that no parent component of target
// (below destDir) is a symlink. extractTar may create absolute symlink
// entries verbatim, so without this check a later regular-file or
// hard-link entry could traverse such a symlink and write outside
// destDir. Components that do not yet exist are safe — MkdirAll
// creates them as real directories. Returns an error naming the
// offending path when a symlinked component is found.
func ensureNoSymlinkParent(destDir, target string) error {
	rel, err := filepath.Rel(destDir, target)
	if err != nil {
		return fmt.Errorf("resolve path against destDir: %w", err)
	}
	parts := strings.Split(rel, string(os.PathSeparator))
	cur := destDir
	// Walk every parent component, excluding the final entry itself.
	for _, p := range parts[:len(parts)-1] {
		if p == "" || p == "." {
			continue
		}
		cur = filepath.Join(cur, p)
		info, err := os.Lstat(cur)
		if err != nil {
			if os.IsNotExist(err) {
				// Component does not exist yet; it (and anything
				// deeper) will be created as real directories.
				return nil
			}
			return fmt.Errorf("stat path component %s: %w", cur, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("illegal path through symlink in archive")
		}
	}
	return nil
}

// extractTar reads entries from a tar reader and extracts them
// to destDir. Validates paths to prevent directory traversal.
func extractTar(tr *tar.Reader, destDir string) error {
	cleanDest := filepath.Clean(destDir) + string(os.PathSeparator)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}

		target := filepath.Join(destDir, hdr.Name) //nolint:gosec // G305 — path validated below

		cleanTarget := filepath.Clean(target)
		if cleanTarget != filepath.Clean(destDir) && !strings.HasPrefix(cleanTarget, cleanDest) {
			return fmt.Errorf("illegal path in archive: %s", hdr.Name)
		}

		// Guard every entry against traversal through a symlink
		// planted earlier in the same archive. extractTar permits
		// absolute symlink entries to be created verbatim (legitimate
		// source tarballs ship them), so a later entry whose path
		// crosses such a symlink could otherwise land outside destDir.
		// Rejecting any path with a symlinked parent component closes
		// that escape while leaving dangling symlinks intact.
		if err := ensureNoSymlinkParent(destDir, target); err != nil {
			return fmt.Errorf("%w: %s", err, hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("create directory %s: %w",
					hdr.Name, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(
				filepath.Dir(target), 0o755,
			); err != nil {
				return fmt.Errorf(
					"create parent directory for %s: %w",
					hdr.Name, err,
				)
			}
			if err := writeFile(target, tr, hdr.FileInfo().Mode()); err != nil {
				return fmt.Errorf("extract %s: %w",
					hdr.Name, err)
			}
		case tar.TypeSymlink:
			// For relative symlinks, validate that the resolved
			// target stays within destDir to prevent traversal.
			// Absolute symlinks pointing outside destDir are
			// written as-is (potentially dangling). They cannot be
			// used to escape because no later write follows a
			// symlinked parent component (see ensureNoSymlinkParent)
			// and file writes use O_NOFOLLOW.
			if !filepath.IsAbs(hdr.Linkname) {
				resolved := filepath.Join(filepath.Dir(target), hdr.Linkname) //nolint:gosec // G305 — validated below
				resolved = filepath.Clean(resolved)
				if !strings.HasPrefix(resolved, cleanDest) {
					return fmt.Errorf("illegal symlink target in archive: %s -> %s",
						hdr.Name, hdr.Linkname)
				}
			}

			if err := os.MkdirAll(
				filepath.Dir(target), 0o755,
			); err != nil {
				return fmt.Errorf(
					"create parent directory for %s: %w",
					hdr.Name, err,
				)
			}
			os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return fmt.Errorf("create symlink %s: %w",
					hdr.Name, err)
			}
		case tar.TypeLink:
			linkTarget := filepath.Join(destDir, hdr.Linkname) //nolint:gosec // G305 — path validated below
			if !strings.HasPrefix(filepath.Clean(linkTarget), cleanDest) {
				return fmt.Errorf("illegal hard link target in archive: %s", hdr.Linkname)
			}
			// Reject a hard-link source that reaches through a
			// symlinked parent — os.Link would otherwise resolve it
			// to a file outside destDir and pull it into the store.
			if err := ensureNoSymlinkParent(destDir, linkTarget); err != nil {
				return fmt.Errorf("%w: %s", err, hdr.Linkname)
			}
			if err := os.MkdirAll(
				filepath.Dir(target), 0o755,
			); err != nil {
				return fmt.Errorf(
					"create parent directory for %s: %w",
					hdr.Name, err,
				)
			}
			os.Remove(target)
			if err := os.Link(linkTarget, target); err != nil {
				return fmt.Errorf("create hard link %s: %w",
					hdr.Name, err)
			}
		case tar.TypeXGlobalHeader, tar.TypeXHeader:
			// PAX headers — skip silently.
			continue
		default:
			return fmt.Errorf("unsupported tar entry type %d for %s",
				hdr.Typeflag, hdr.Name)
		}
	}

	return nil
}

// writeFile creates a file at path, copies content from r,
// and sets the given file mode.
func writeFile(path string, r io.Reader, mode os.FileMode) error {
	// O_NOFOLLOW rejects a final path component that is itself a
	// symlink, so a regular-file entry sharing a name with a
	// previously extracted symlink cannot follow it and clobber a
	// target outside destDir.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, mode)
	if err != nil {
		return err
	}

	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}

	return f.Close()
}
