package download

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// buildTarZstdBytes creates a tar.zst archive in memory from a
// files map (relative path -> content) and returns the raw bytes.
func buildTarZstdBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf, zstd.WithEncoderConcurrency(1))
	if err != nil {
		t.Fatalf("create zstd writer: %v", err)
	}
	tw := tar.NewWriter(zw)

	writeTarEntries(t, tw, files)

	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zstd writer: %v", err)
	}
	return buf.Bytes()
}

// hexSHA256 returns the hex-encoded SHA256 of b.
func hexSHA256(b []byte) string {
	h := sha256.Sum256(b)
	return fmt.Sprintf("%x", h)
}

// destIsEmpty returns true when destDir either does not exist or
// exists but contains no children.
func destIsEmpty(t *testing.T, destDir string) bool {
	t.Helper()
	entries, err := os.ReadDir(destDir)
	if os.IsNotExist(err) {
		return true
	}
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", destDir, err)
	}
	return len(entries) == 0
}

// --- Behaviour 1: Good archive streams to disk with correct contents ---

func TestFetchAndExtractTarZstdStreamsCorrectContents(t *testing.T) {
	restore := SetProgressEnabled(false)
	defer restore()

	archiveBytes := buildTarZstdBytes(t, map[string]string{
		"bin/foo":        "#!/bin/sh\necho foo",
		"share/data.txt": "hello from data",
	})
	correctSHA := hexSHA256(archiveBytes)

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write(archiveBytes)
		}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "pkg")

	gotSHA, err := FetchAndExtractTarZstd(srv.URL, dest, correctSHA, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotSHA != correctSHA {
		t.Errorf("returned SHA = %q, want %q", gotSHA, correctSHA)
	}

	// Both files must be present with expected contents.
	fooData, err := os.ReadFile(filepath.Join(dest, "bin", "foo"))
	if err != nil {
		t.Fatalf("bin/foo not extracted: %v", err)
	}
	if string(fooData) != "#!/bin/sh\necho foo" {
		t.Errorf("bin/foo = %q, want %q", fooData, "#!/bin/sh\necho foo")
	}

	dataData, err := os.ReadFile(filepath.Join(dest, "share", "data.txt"))
	if err != nil {
		t.Fatalf("share/data.txt not extracted: %v", err)
	}
	if string(dataData) != "hello from data" {
		t.Errorf("share/data.txt = %q, want %q", dataData, "hello from data")
	}
}

// --- Behaviour 2: Bad expected SHA returns error and cleans up dest ---

func TestFetchAndExtractTarZstdBadSHACleansUpDest(t *testing.T) {
	restore := SetProgressEnabled(false)
	defer restore()

	archiveBytes := buildTarZstdBytes(t, map[string]string{
		"bin/tool": "#!/bin/sh\necho tool",
	})
	wrongSHA := strings.Repeat("0", 64)

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Write(archiveBytes)
		}))
	defer srv.Close()

	// Pre-create dest with a sentinel file so that a stub that does
	// nothing would leave the sentinel behind, causing the test to fail RED.
	dest := filepath.Join(t.TempDir(), "pkg")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinelPath := filepath.Join(dest, "PREEXISTING.txt")
	if err := os.WriteFile(sentinelPath, []byte("should-be-removed"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := FetchAndExtractTarZstd(srv.URL, dest, wrongSHA, "")
	if err == nil {
		t.Fatal("expected error for SHA mismatch, got nil")
	}

	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "sha") &&
		!strings.Contains(msg, "hash") &&
		!strings.Contains(msg, "mismatch") {
		t.Errorf("error %q should mention sha/hash/mismatch", err.Error())
	}

	// Cleanup must have removed the sentinel.
	if _, statErr := os.Stat(sentinelPath); statErr == nil {
		t.Errorf("dest not cleaned up: sentinel still present at %s", sentinelPath)
	}
}

// --- Behaviour 3: HTTP 404 returns error, dest is clean ---

func TestFetchAndExtractTarZstdHTTP404ReturnsError(t *testing.T) {
	restore := SetProgressEnabled(false)
	defer restore()

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "not found", http.StatusNotFound)
		}))
	defer srv.Close()

	// Pre-create dest with a sentinel file so that a stub that does
	// nothing would leave the sentinel behind, causing the test to fail RED.
	dest := filepath.Join(t.TempDir(), "pkg")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinelPath := filepath.Join(dest, "PREEXISTING.txt")
	if err := os.WriteFile(sentinelPath, []byte("should-be-removed"), 0o644); err != nil {
		t.Fatal(err)
	}
	anySHA := strings.Repeat("a", 64)

	_, err := FetchAndExtractTarZstd(srv.URL, dest, anySHA, "")
	if err == nil {
		t.Fatal("expected error for HTTP 404, got nil")
	}

	// Cleanup must have removed the sentinel.
	if _, statErr := os.Stat(sentinelPath); statErr == nil {
		t.Errorf("dest not cleaned up: sentinel still present at %s", sentinelPath)
	}
}

// --- Behaviour 4a: Bearer token header is sent when token != "" ---

func TestFetchAndExtractTarZstdSendsBearerTokenWhenProvided(t *testing.T) {
	restoreProgress := SetProgressEnabled(false)
	defer restoreProgress()

	archiveBytes := buildTarZstdBytes(t, map[string]string{
		"bin/tool": "data",
	})
	correctSHA := hexSHA256(archiveBytes)

	var capturedAuth string
	// Use TLS server: the production code refuses to send a bearer
	// token over plain HTTP (same policy as FetchWithAuthNamed).
	srv := httptest.NewTLSServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			capturedAuth = r.Header.Get("Authorization")
			w.Write(archiveBytes) //nolint:errcheck
		}))
	defer srv.Close()

	// Make the package-level HTTP client trust the test server's
	// self-signed certificate (same pattern as FetchWithAuth tests).
	restoreClient := SetHTTPClient(srv.Client())
	defer restoreClient()

	dest := filepath.Join(t.TempDir(), "pkg")
	token := "super-secret-token-xyz"

	_, err := FetchAndExtractTarZstd(srv.URL, dest, correctSHA, token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedAuth != "Bearer "+token {
		t.Errorf("Authorization = %q, want %q", capturedAuth, "Bearer "+token)
	}
}

// --- Behaviour 4b: No Authorization header when token is empty ---

func TestFetchAndExtractTarZstdNoAuthHeaderWhenTokenEmpty(t *testing.T) {
	restore := SetProgressEnabled(false)
	defer restore()

	archiveBytes := buildTarZstdBytes(t, map[string]string{
		"bin/notokenfile": "no-token-content-abc",
	})
	correctSHA := hexSHA256(archiveBytes)

	var capturedAuth string
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			capturedAuth = r.Header.Get("Authorization")
			w.Write(archiveBytes)
		}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "pkg")

	_, err := FetchAndExtractTarZstd(srv.URL, dest, correctSHA, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Guard: extraction must have actually occurred. Without this
	// the no-auth assertion is vacuous against the stub (which
	// never makes an HTTP request so capturedAuth stays "").
	if _, statErr := os.Stat(filepath.Join(dest, "bin", "notokenfile")); statErr != nil {
		t.Fatalf("bin/notokenfile not extracted: %v", statErr)
	}

	if capturedAuth != "" {
		t.Errorf("Authorization = %q, want empty string when no token given", capturedAuth)
	}
}

// --- Behaviour 5: Dest dir is created if it doesn't exist ---

func TestFetchAndExtractTarZstdCreatesDestDirIfAbsent(t *testing.T) {
	restore := SetProgressEnabled(false)
	defer restore()

	archiveBytes := buildTarZstdBytes(t, map[string]string{
		"bin/tool": "#!/bin/sh\necho hi",
	})
	correctSHA := hexSHA256(archiveBytes)

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Write(archiveBytes)
		}))
	defer srv.Close()

	// Use a path that does NOT yet exist.
	dest := filepath.Join(t.TempDir(), "newdir")
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("expected %s to not exist before the call", dest)
	}

	_, err := FetchAndExtractTarZstd(srv.URL, dest, correctSHA, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// dest must now exist and contain extracted files.
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("dest dir should exist after success: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "bin", "tool")); err != nil {
		t.Fatalf("bin/tool should be present after extraction: %v", err)
	}
}

// --- Behaviour 6: No on-disk intermediate .tar.zst file is left behind ---

func TestFetchAndExtractTarZstdLeavesNoIntermediateTarZstFile(t *testing.T) {
	restore := SetProgressEnabled(false)
	defer restore()

	archiveBytes := buildTarZstdBytes(t, map[string]string{
		"bin/tool": "#!/bin/sh\necho streaming",
	})
	correctSHA := hexSHA256(archiveBytes)

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Write(archiveBytes)
		}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "pkg")

	_, err := FetchAndExtractTarZstd(srv.URL, dest, correctSHA, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Assert extraction actually occurred (guards the no-tmpfile
	// claim — if dest is empty the test is vacuous).
	if _, err := os.Stat(filepath.Join(dest, "bin", "tool")); err != nil {
		t.Fatalf("bin/tool not extracted: %v", err)
	}

	// No sibling .tar.zst or .download.tar.zst file should exist
	// next to dest. The legacy tmpFile pattern is
	// dest + ".download.tar.zst".
	parent := filepath.Dir(dest)
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", parent, err)
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".tar.zst") || strings.HasSuffix(name, ".download.tar.zst") {
			t.Errorf("unexpected intermediate file in parent dir: %s", name)
		}
	}
}

// --- Behaviour 7: Computed SHA is returned on success ---

func TestFetchAndExtractTarZstdReturnsSHA256OfArchiveBytes(t *testing.T) {
	restore := SetProgressEnabled(false)
	defer restore()

	archiveBytes := buildTarZstdBytes(t, map[string]string{
		"bin/mytool": "#!/bin/sh\necho mytool",
	})
	// Compute the expected SHA directly from the same bytes the
	// server will serve. This is distinct from all-zeros or the
	// empty-string default the stub returns.
	expectedSHA := hexSHA256(archiveBytes)

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Write(archiveBytes)
		}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "pkg")

	gotSHA, err := FetchAndExtractTarZstd(srv.URL, dest, expectedSHA, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The returned SHA must equal the SHA256 of the bytes served.
	// The stub returns "" which != expectedSHA, making this RED.
	if gotSHA != expectedSHA {
		t.Errorf("returned SHA = %q, want %q", gotSHA, expectedSHA)
	}
	// Also verify it is a valid 64-char hex string.
	if len(gotSHA) != 64 {
		t.Errorf("returned SHA length = %d, want 64", len(gotSHA))
	}
}

// --- Behaviour 8: EOF/truncated stream returns error, dest cleaned ---

func TestFetchAndExtractTarZstdTruncatedStreamReturnsErrorAndCleansUp(t *testing.T) {
	restore := SetProgressEnabled(false)
	defer restore()

	archiveBytes := buildTarZstdBytes(t, map[string]string{
		"bin/tool": "#!/bin/sh\necho trunc",
	})

	// Build a correct SHA for the full archive. The truncated
	// stream will not match it, so the hash check (or the
	// extractor mid-stream failure) must return an error.
	correctSHA := hexSHA256(archiveBytes)

	// Server sends only the first half of the bytes then closes.
	half := archiveBytes[:len(archiveBytes)/2]
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			// Advertise the full length so the client expects more
			// bytes than it will receive.
			w.Header().Set("Content-Length",
				strconv.Itoa(len(archiveBytes)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(half)
			// Flush so the partial bytes leave the kernel buffer
			// before we abruptly close the connection.
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			// Hijack and close to force the client to see an
			// unexpected EOF mid-stream.
			if hj, ok := w.(http.Hijacker); ok {
				conn, _, herr := hj.Hijack()
				if herr == nil && conn != nil {
					_ = conn.Close()
				}
			}
		}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "pkg")

	_, err := FetchAndExtractTarZstd(srv.URL, dest, correctSHA, "")
	if err == nil {
		t.Fatal("expected error for truncated stream, got nil")
	}

	if !destIsEmpty(t, dest) {
		t.Errorf("dest %q should be cleaned up after truncated stream", dest)
	}
}

// --- Behaviour 7 (explicit): large distinct archive produces distinct SHA ---

func TestFetchAndExtractTarZstdDistinctArchivesProduceDistinctSHAs(t *testing.T) {
	restore := SetProgressEnabled(false)
	defer restore()

	// Two archives with different contents produce different SHAs.
	archiveA := buildTarZstdBytes(t, map[string]string{
		"bin/alpha": "alpha content unique abc123",
	})
	archiveB := buildTarZstdBytes(t, map[string]string{
		"bin/beta": "beta content unique xyz789",
	})

	shaA := hexSHA256(archiveA)
	shaB := hexSHA256(archiveB)
	if shaA == shaB {
		t.Fatal("test setup error: both archives have the same SHA256")
	}

	srvA := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Write(archiveA)
		}))
	defer srvA.Close()

	srvB := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Write(archiveB)
		}))
	defer srvB.Close()

	destA := filepath.Join(t.TempDir(), "pkgA")
	destB := filepath.Join(t.TempDir(), "pkgB")

	gotA, err := FetchAndExtractTarZstd(srvA.URL, destA, shaA, "")
	if err != nil {
		t.Fatalf("archive A: unexpected error: %v", err)
	}
	gotB, err := FetchAndExtractTarZstd(srvB.URL, destB, shaB, "")
	if err != nil {
		t.Fatalf("archive B: unexpected error: %v", err)
	}

	if gotA != shaA {
		t.Errorf("archive A: returned SHA = %q, want %q", gotA, shaA)
	}
	if gotB != shaB {
		t.Errorf("archive B: returned SHA = %q, want %q", gotB, shaB)
	}
	if gotA == gotB {
		t.Errorf("distinct archives produced identical SHAs: %q", gotA)
	}
}
