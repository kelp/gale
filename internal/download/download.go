package download

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/kelp/gale/internal/httpclient"
)

// httpBase is the client tests swap via SetHTTPClient. Production
// uses httpclient.Default so registry fetches keep the shared pool.
// Each fetch derives a policy client from this base: unauth gets
// hop-validated CheckRedirect; auth gets userinfo + no
// https→http. The hop map never rides Default itself.
var httpBase = httpclient.Default()

// SetHTTPClient replaces the base HTTP client.
// Intended for tests that need a custom TLS configuration
// (e.g., httptest.NewTLSServer). Policy CheckRedirect is
// reapplied on every fetch so a swap cannot drop it.
// Returns a function that restores the original client.
func SetHTTPClient(c *http.Client) func() {
	saved := httpBase
	httpBase = c
	return func() { httpBase = saved }
}

func unauthClient() *http.Client {
	return &http.Client{
		Transport:     httpBase.Transport,
		Timeout:       httpBase.Timeout,
		CheckRedirect: httpclient.CheckRedirect,
	}
}

func authClient() *http.Client {
	return &http.Client{
		Transport:     httpBase.Transport,
		Timeout:       httpBase.Timeout,
		CheckRedirect: httpclient.AuthCheckRedirect,
	}
}

func rejectCredentials(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse URL: %w", err)
	}
	if u.User != nil {
		return fmt.Errorf("url must not contain credentials")
	}
	return nil
}

// Fetch downloads a file from rawURL to destPath.
// Intermediate directories are created as needed.
// On HTTP error or failure, the destination file is removed.
// Fetch never reads GALE_GITHUB_TOKEN or GITHUB_TOKEN.
func Fetch(ctx context.Context, rawURL, destPath string) error {
	if err := rejectCredentials(rawURL); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}
	return fetchOnce(ctx, rawURL, destPath, unauthClient(), "")
}

// FetchWithAuth downloads a file from rawURL to destPath with a
// bearer token in the Authorization header. HTTPS is required so
// the token is never sent in the clear. The token is an explicit
// argument; this function does not read the environment.
func FetchWithAuth(ctx context.Context, rawURL, destPath, bearerToken string) error {
	if err := rejectCredentials(rawURL); err != nil {
		return err
	}
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
	return fetchOnce(ctx, rawURL, destPath, authClient(), bearerToken)
}

// fetchOnce performs a single HTTP GET and writes to destPath.
func fetchOnce(ctx context.Context, rawURL, destPath string, client *http.Client, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch %s: %w", rawURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("fetch %s: HTTP %d", rawURL, resp.StatusCode)
	}

	name := filepath.Base(rawURL)
	return writeWithProgress(ctx, resp.Body, resp.ContentLength, destPath, name)
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
func writeWithProgress(ctx context.Context, reader io.Reader, total int64, destPath, name string) error {
	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("create destination file: %w", err)
	}

	pw := &progressWriter{
		total: total,
		start: time.Now(),
		name:  name,
	}
	body := ctxReader{ctx: ctx, r: limitCompressed(reader)}
	if _, err := io.Copy(f, io.TeeReader(body, pw)); err != nil {
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
func HashFile(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open file for hashing: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, ctxReader{ctx: ctx, r: f}); err != nil {
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
func VerifySHA256(ctx context.Context, path, expected string) error {
	actual, err := HashFile(ctx, path)
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
