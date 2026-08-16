package download

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/kelp/gale/internal/httpclient"
)

// httpClient serves all download traffic. It is the process-wide
// shared client from internal/httpclient, which deliberately has
// no whole-transfer Timeout: a hard cap (formerly 5 minutes here)
// aborts large GHCR blob and source tarball transfers mid-stream
// on slow links (gh#61). Stalled connections are still bounded by
// the transport's dial and TLS handshake timeouts; callers that
// need a per-request deadline pass one via context.
var httpClient = httpclient.Default()

// mirrors maps URL prefixes to fallback mirror prefixes.
// When a download fails with an HTTP error, the URL prefix
// is replaced with each fallback in order until one succeeds.
var mirrors = map[string][]string{
	"https://ftpmirror.gnu.org/": {
		"https://mirrors.kernel.org/gnu/",
		"https://ftp.gnu.org/pub/gnu/",
	},
	"https://ftp.gnu.org/gnu/": {
		"https://mirrors.kernel.org/gnu/",
		"https://ftpmirror.gnu.org/",
	},
}

// SetHTTPClient replaces the package-level HTTP client.
// Intended for tests that need a custom TLS configuration
// (e.g., httptest.NewTLSServer). Returns a function that
// restores the original client.
func SetHTTPClient(c *http.Client) func() {
	saved := httpClient
	httpClient = c
	return func() { httpClient = saved }
}

// Fetch downloads a file from rawURL to destPath.
// Intermediate directories are created as needed.
// On HTTP error or failure, the destination file is removed.
// If the primary URL fails, known mirror fallbacks are tried.
func Fetch(rawURL, destPath string) error {
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}

	err := fetchOnce(rawURL, destPath)
	if err == nil {
		return nil
	}

	// Try mirror fallbacks.
	for prefix, fallbacks := range mirrors {
		if !strings.HasPrefix(rawURL, prefix) {
			continue
		}
		suffix := rawURL[len(prefix):]
		for _, fb := range fallbacks {
			alt := fb + suffix
			fmt.Fprintf(os.Stderr,
				"  > Mirror fallback: %s\n", alt)
			if ferr := fetchOnce(alt, destPath); ferr == nil {
				fmt.Fprintf(os.Stderr,
					"  > Mirror fetched from: %s\n", alt)
				return nil
			}
		}
	}

	return err
}

// FetchWithAuth downloads a file from rawURL to destPath with a
// bearer token in the Authorization header. HTTPS is required so
// the token is never sent in the clear. No mirror fallbacks: the
// token is scoped to the primary host.
func FetchWithAuth(rawURL, destPath, bearerToken string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse URL: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf(
			"refusing to send bearer token over %s (https required)",
			u.Scheme,
		)
	}

	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}

	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearerToken)

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetch %s: %w", rawURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("fetch %s: HTTP %d", rawURL, resp.StatusCode)
	}

	name := filepath.Base(rawURL)
	return writeWithProgress(resp.Body, resp.ContentLength, destPath, name)
}

// fetchOnce performs a single HTTP GET and writes to destPath.
func fetchOnce(rawURL, destPath string) error {
	resp, err := httpClient.Get(rawURL) //nolint:gosec // G107 — URL is caller-provided
	if err != nil {
		return fmt.Errorf("fetch %s: %w", rawURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("fetch %s: HTTP %d", rawURL, resp.StatusCode)
	}

	name := filepath.Base(rawURL)
	return writeWithProgress(resp.Body, resp.ContentLength, destPath, name)
}

// ProgressPrefix is the colored prefix used for download
// progress lines. Set by the build module to match the
// output style.
var ProgressPrefix = "  > "

// ProgressEnabled controls whether incremental download
// progress is printed to stderr.
var ProgressEnabled = true

// SetProgressEnabled updates ProgressEnabled and returns a
// restore function for tests and temporary overrides.
func SetProgressEnabled(enabled bool) func() {
	saved := ProgressEnabled
	ProgressEnabled = enabled
	return func() { ProgressEnabled = saved }
}

// writeWithProgress copies from reader to a file at destPath,
// printing download progress to stderr.
func writeWithProgress(reader io.Reader, total int64, destPath, name string) error {
	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("create destination file: %w", err)
	}

	pw := &progressWriter{
		total: total,
		start: time.Now(),
		name:  name,
	}
	if _, err := io.Copy(f, io.TeeReader(limitCompressed(reader), pw)); err != nil {
		f.Close()
		os.Remove(destPath)
		return fmt.Errorf("write destination file: %w", err)
	}
	pw.finish()

	if err := f.Close(); err != nil {
		os.Remove(destPath)
		return fmt.Errorf("close destination file: %w", err)
	}

	return nil
}

// progressWriter prints download progress to stderr.
type progressWriter struct {
	written int64
	total   int64 // -1 if unknown
	start   time.Time
	last    time.Time
	name    string
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	n := len(p)
	pw.written += int64(n)
	if !ProgressEnabled {
		return n, nil
	}

	now := time.Now()
	if now.Sub(pw.last) < 250*time.Millisecond {
		return n, nil
	}
	pw.last = now

	elapsed := now.Sub(pw.start).Seconds()
	if elapsed == 0 {
		return n, nil
	}
	speed := float64(pw.written) / elapsed

	var line string
	if pw.total > 0 {
		pct := float64(pw.written) / float64(pw.total) * 100
		line = fmt.Sprintf("%sDownloading - %s %s / %s (%3.0f%%) %s/s",
			ProgressPrefix, pw.name,
			formatBytes(pw.written), formatBytes(pw.total),
			pct, formatBytes(int64(speed)))
	} else {
		line = fmt.Sprintf("%sDownloading - %s %s  %s/s",
			ProgressPrefix, pw.name,
			formatBytes(pw.written), formatBytes(int64(speed)))
	}
	// Truncate to 80 columns to prevent line wrapping
	// (which breaks \r carriage return).
	const maxWidth = 80
	if len(line) > maxWidth {
		line = line[:maxWidth]
	}
	// Pad to clear previous longer lines.
	for len(line) < maxWidth {
		line += " "
	}
	fmt.Fprintf(os.Stderr, "\r%s", line)

	return n, nil
}

func (pw *progressWriter) finish() {
	if !ProgressEnabled {
		return
	}
	elapsed := time.Since(pw.start).Seconds()
	if elapsed == 0 {
		elapsed = 0.001
	}
	speed := float64(pw.written) / elapsed
	line := fmt.Sprintf("%sDownloaded - %s %s in %.1fs (%s/s)",
		ProgressPrefix, pw.name,
		formatBytes(pw.written), elapsed,
		formatBytes(int64(speed)))
	for len(line) < 70 {
		line += " "
	}
	fmt.Fprintf(os.Stderr, "\r%s\n", line)
}

func formatBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// HashFile returns the hex-encoded SHA256 hash of the
// file at the given path.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open file for hashing: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash file: %w", err)
	}

	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// ErrSHA256Mismatch reports fetched bytes whose hash is not the one
// that was expected. A sentinel because the exit-code taxonomy needs
// to tell this apart from every other reason a fetch can fail: under a
// lock, design §8 puts an artifact SHA mismatch in the integrity class
// that stops a pipeline for a human, while a 404 or a refused
// connection is an ordinary failure.
var ErrSHA256Mismatch = errors.New("sha256 mismatch")

// VerifySHA256 checks that the file at path has the expected
// SHA256 hash. The expected value must be hex-encoded.
func VerifySHA256(path, expected string) error {
	actual, err := HashFile(path)
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf(
			"%w: expected %s, got %s",
			ErrSHA256Mismatch, expected, actual,
		)
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

	zw, err := zstd.NewWriter(f, zstd.WithEncoderConcurrency(1))
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

			// Convert absolute symlink targets within the
			// source tree to relative paths. Absolute paths
			// from make install (e.g., ln -s -f /tmp/build/
			// prefix/bin/tool bin/alias) break after
			// extraction and make archives non-deterministic.
			if filepath.IsAbs(target) {
				absSource, _ := filepath.Abs(sourceDir)
				if strings.HasPrefix(target, absSource+string(os.PathSeparator)) {
					// Target is inside the source tree.
					// Make it relative to the symlink's dir.
					linkDir := filepath.Dir(path)
					relTarget, relErr := filepath.Rel(linkDir, target)
					if relErr == nil {
						target = relTarget
					}
				}
			}

			hdr := &tar.Header{
				Typeflag: tar.TypeSymlink,
				Name:     rel,
				Linkname: filepath.ToSlash(target),
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

		if err := copyFileToTar(tw, path, rel); err != nil {
			return err
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

// copyFileToTar opens a file, copies it into a tar writer,
// and closes the file immediately. This avoids deferring
// Close inside a filepath.Walk callback, which would leak
// file descriptors until the outer function returns.
func copyFileToTar(tw *tar.Writer, path, rel string) error {
	src, err := os.Open(path) //nolint:gosec // G304 — path comes from Walk
	if err != nil {
		return fmt.Errorf("open source file %s: %w", rel, err)
	}
	defer src.Close()

	if _, err := io.Copy(tw, src); err != nil {
		return fmt.Errorf("write file content %s: %w", rel, err)
	}
	return nil
}

// FetchAndExtractTarZstd streams a .tar.zst HTTP response
// directly through a SHA-256 hasher and a tar.zst extractor in
// one pass — no on-disk intermediate file. Verifies the computed
// hash against expectedSHA256 at end of stream; on mismatch the
// partially-extracted destDir is cleaned up before returning the
// error. token is an optional Bearer authorization header value
// (empty string = no Authorization header sent). Returns the
// computed hex SHA-256 on success.
func FetchAndExtractTarZstd(rawURL, destDir, expectedSHA256, token string) (string, error) {
	return fetchAndExtractTarZstd(rawURL, destDir, expectedSHA256, token, "")
}

// FetchAndExtractTarZstdWithArchive is like FetchAndExtractTarZstd
// but also writes the raw compressed archive bytes to archiveOut
// (when non-empty) as they stream. The caller owns the file's
// lifecycle (creation and deletion); this function only writes to it.
func FetchAndExtractTarZstdWithArchive(rawURL, destDir, expectedSHA256, token, archiveOut string) (string, error) {
	return fetchAndExtractTarZstd(rawURL, destDir, expectedSHA256, token, archiveOut)
}

// fetchAndExtractTarZstd is the shared implementation used by both
// FetchAndExtractTarZstd and FetchAndExtractTarZstdWithArchive.
// When archiveOut is non-empty the raw (compressed) bytes are also
// written to that file as they stream, enabling callers to verify
// the archive digest (e.g. Sigstore attestation) after extraction.
func fetchAndExtractTarZstd(rawURL, destDir, expectedSHA256, token, archiveOut string) (string, error) {
	if token != "" {
		u, err := url.Parse(rawURL)
		if err != nil {
			return "", fmt.Errorf("parse URL: %w", err)
		}
		if u.Scheme != "https" {
			return "", fmt.Errorf(
				"refusing to send bearer token over %s (https required)",
				u.Scheme,
			)
		}
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", rawURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = os.RemoveAll(destDir)
		return "", fmt.Errorf("fetch %s: HTTP %d", rawURL, resp.StatusCode)
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", fmt.Errorf("create destination directory: %w", err)
	}

	hasher := sha256.New()
	sinks := []io.Writer{hasher}
	if archiveOut != "" {
		af, err := os.Create(archiveOut)
		if err != nil {
			_ = os.RemoveAll(destDir)
			return "", fmt.Errorf("create archive copy: %w", err)
		}
		defer af.Close()
		sinks = append(sinks, af)
	}
	teed := io.TeeReader(limitCompressed(resp.Body), io.MultiWriter(sinks...))

	zr, err := zstd.NewReader(teed)
	if err != nil {
		_ = os.RemoveAll(destDir)
		return "", fmt.Errorf("create zstd reader: %w", err)
	}
	defer zr.Close()

	b := newBudget()
	if err := extractTar(tar.NewReader(b.reader(zr)), destDir, b, classBuildInput); err != nil {
		_ = os.RemoveAll(destDir)
		return "", fmt.Errorf("extract: %w", err)
	}

	computed := fmt.Sprintf("%x", hasher.Sum(nil))
	if !strings.EqualFold(computed, expectedSHA256) {
		_ = os.RemoveAll(destDir)
		return "", fmt.Errorf("%w: expected %s, got %s",
			ErrSHA256Mismatch, expectedSHA256, computed)
	}

	return computed, nil
}
