package download

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// Fetch downloads a file from url to destPath.
// Intermediate directories are created as needed.
// On HTTP error or failure, the destination file is removed.
func Fetch(url, destPath string) error {
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}

	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("fetch %s: HTTP %d", url, resp.StatusCode)
	}

	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("create destination file: %w", err)
	}

	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(destPath)
		return fmt.Errorf("write destination file: %w", err)
	}

	if err := f.Close(); err != nil {
		os.Remove(destPath)
		return fmt.Errorf("close destination file: %w", err)
	}

	return nil
}

// FetchWithAuth downloads a file from url to destPath with a
// bearer token in the Authorization header.
func FetchWithAuth(url, destPath, bearerToken string) error {
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearerToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("fetch %s: HTTP %d", url, resp.StatusCode)
	}

	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("create destination file: %w", err)
	}

	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(destPath)
		return fmt.Errorf("write destination file: %w", err)
	}

	if err := f.Close(); err != nil {
		os.Remove(destPath)
		return fmt.Errorf("close destination file: %w", err)
	}

	return nil
}

// VerifySHA256 checks that the file at path has the expected
// SHA256 hash. The expected value must be hex-encoded. On
// mismatch the error contains both the expected and actual hashes.
func VerifySHA256(path, expected string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open file for hashing: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hash file: %w", err)
	}

	actual := fmt.Sprintf("%x", h.Sum(nil))
	if actual != expected {
		return fmt.Errorf(
			"sha256 mismatch: expected %s, got %s",
			expected, actual)
	}

	return nil
}

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
			filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf(
				"create parent directory for %s: %w",
				zf.Name, err)
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

		if !strings.HasPrefix(filepath.Clean(target), cleanDest) {
			return fmt.Errorf("illegal path in archive: %s", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("create directory %s: %w",
					hdr.Name, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(
				filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf(
					"create parent directory for %s: %w",
					hdr.Name, err)
			}
			if err := writeFile(target, tr, hdr.FileInfo().Mode()); err != nil {
				return fmt.Errorf("extract %s: %w",
					hdr.Name, err)
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(
				filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf(
					"create parent directory for %s: %w",
					hdr.Name, err)
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
			if err := os.MkdirAll(
				filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf(
					"create parent directory for %s: %w",
					hdr.Name, err)
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

// CreateTarZstd creates a tar.zst archive from sourceDir.
// Files are stored relative to the sourceDir root with no
// wrapper directory. File permissions are preserved.
func CreateTarZstd(sourceDir, archivePath string) error {
	f, err := os.Create(archivePath)
	if err != nil {
		return fmt.Errorf("create archive file: %w", err)
	}
	defer f.Close()

	zw, err := zstd.NewWriter(f)
	if err != nil {
		return fmt.Errorf("create zstd writer: %w", err)
	}
	defer zw.Close()

	tw := tar.NewWriter(zw)
	defer tw.Close()

	err = filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip the root directory itself.
		if path == sourceDir {
			return nil
		}

		rel, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return fmt.Errorf("compute relative path: %w", err)
		}
		// Use forward slashes in the archive.
		rel = filepath.ToSlash(rel)

		// Check for symlinks via Lstat (Walk uses Stat which follows them).
		linfo, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("lstat %s: %w", rel, err)
		}

		if linfo.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return fmt.Errorf("readlink %s: %w", rel, err)
			}
			hdr := &tar.Header{
				Typeflag: tar.TypeSymlink,
				Name:     rel,
				Linkname: target,
				Mode:     int64(linfo.Mode()),
			}
			return tw.WriteHeader(hdr)
		}

		if info.IsDir() {
			hdr := &tar.Header{
				Typeflag: tar.TypeDir,
				Name:     rel + "/",
				Mode:     int64(info.Mode()),
			}
			if err := tw.WriteHeader(hdr); err != nil {
				return fmt.Errorf("write dir header %s: %w", rel, err)
			}
			return nil
		}

		hdr := &tar.Header{
			Typeflag: tar.TypeReg,
			Name:     rel,
			Size:     linfo.Size(),
			Mode:     int64(linfo.Mode()),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("write file header %s: %w", rel, err)
		}

		src, err := os.Open(path) //nolint:gosec // G122 — Walk callback, race is acceptable for archive creation
		if err != nil {
			return fmt.Errorf("open source file %s: %w", rel, err)
		}
		defer src.Close()

		if _, err := io.Copy(tw, src); err != nil {
			return fmt.Errorf("write file content %s: %w", rel, err)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("walk source directory: %w", err)
	}

	// Close in reverse order: tar, then zstd, then file.
	if err := tw.Close(); err != nil {
		return fmt.Errorf("close tar writer: %w", err)
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("close zstd writer: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close archive file: %w", err)
	}

	return nil
}

// writeFile creates a file at path, copies content from r,
// and sets the given file mode.
func writeFile(path string, r io.Reader, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
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
