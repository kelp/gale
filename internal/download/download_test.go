package download

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

// --- Behavior 0: HTTP client has timeout ---

func TestHTTPClientHasTimeout(t *testing.T) {
	if httpClient.Timeout == 0 {
		t.Fatal("httpClient.Timeout must be non-zero")
	}
}

// --- Behavior 1: Download file from URL ---

func TestFetchWritesFileToDestPath(t *testing.T) {
	want := "hello from the server"
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, want)
		}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "downloaded.txt")

	if err := Fetch(srv.URL, dest); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("failed to read dest file: %v", err)
	}
	if string(got) != want {
		t.Errorf("file contents = %q, want %q", string(got), want)
	}
}

// --- Behavior 2: SHA256 verification ---

func TestVerifySHA256CorrectHash(t *testing.T) {
	content := []byte("known content for hashing")
	h := sha256.Sum256(content)
	expected := fmt.Sprintf("%x", h)

	path := filepath.Join(t.TempDir(), "hashme.txt")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	if err := VerifySHA256(path, expected); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestVerifySHA256WrongHashReturnsError(t *testing.T) {
	content := []byte("some content")

	path := filepath.Join(t.TempDir(), "hashme.txt")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	wrongHash := "0000000000000000000000000000000000000000000000000000000000000000"
	err := VerifySHA256(path, wrongHash)
	if err == nil {
		t.Fatal("expected error for wrong hash")
	}
}

func TestVerifySHA256ErrorContainsBothHashes(t *testing.T) {
	content := []byte("hash mismatch content")
	h := sha256.Sum256(content)
	actual := fmt.Sprintf("%x", h)

	path := filepath.Join(t.TempDir(), "hashme.txt")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	wrongHash := "0000000000000000000000000000000000000000000000000000000000000000"
	err := VerifySHA256(path, wrongHash)
	if err == nil {
		t.Fatal("expected error for wrong hash")
	}

	msg := err.Error()
	if !strings.Contains(msg, wrongHash) {
		t.Errorf("error should contain expected hash %q, got %q",
			wrongHash, msg)
	}
	if !strings.Contains(msg, actual) {
		t.Errorf("error should contain actual hash %q, got %q",
			actual, msg)
	}
}

func TestVerifySHA256NonexistentFileReturnsError(t *testing.T) {
	err := VerifySHA256("/nonexistent/path/file.txt", "abc123")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

// --- Behavior 3: Extract tar.gz ---

func TestExtractTarGzPreservesFileContents(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "test.tar.gz")
	createTarGz(t, archivePath, map[string]string{
		"hello.txt": "hello world",
	})

	destDir := filepath.Join(t.TempDir(), "extracted")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("failed to create dest dir: %v", err)
	}

	if err := ExtractTarGz(archivePath, destDir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(destDir, "hello.txt"))
	if err != nil {
		t.Fatalf("failed to read extracted file: %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("file contents = %q, want %q",
			string(got), "hello world")
	}
}

func TestExtractTarGzPreservesRelativePaths(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "test.tar.gz")
	createTarGz(t, archivePath, map[string]string{
		"subdir/nested.txt": "nested content",
	})

	destDir := filepath.Join(t.TempDir(), "extracted")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("failed to create dest dir: %v", err)
	}

	if err := ExtractTarGz(archivePath, destDir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(
		filepath.Join(destDir, "subdir", "nested.txt"))
	if err != nil {
		t.Fatalf("failed to read extracted file: %v", err)
	}
	if string(got) != "nested content" {
		t.Errorf("file contents = %q, want %q",
			string(got), "nested content")
	}
}

func TestExtractTarGzMultipleFiles(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "test.tar.gz")
	createTarGz(t, archivePath, map[string]string{
		"a.txt": "aaa",
		"b.txt": "bbb",
	})

	destDir := filepath.Join(t.TempDir(), "extracted")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("failed to create dest dir: %v", err)
	}

	if err := ExtractTarGz(archivePath, destDir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, name := range []string{"a.txt", "b.txt"} {
		if _, err := os.Stat(
			filepath.Join(destDir, name)); err != nil {
			t.Errorf("expected file %q to exist: %v", name, err)
		}
	}
}

// --- Behavior 4: Extract zip ---

func TestExtractZipPreservesFileContents(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "test.zip")
	createZip(t, archivePath, map[string]string{
		"hello.txt": "hello world",
	})

	destDir := filepath.Join(t.TempDir(), "extracted")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("failed to create dest dir: %v", err)
	}

	if err := ExtractZip(archivePath, destDir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(destDir, "hello.txt"))
	if err != nil {
		t.Fatalf("failed to read extracted file: %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("file contents = %q, want %q",
			string(got), "hello world")
	}
}

func TestExtractZipPreservesRelativePaths(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "test.zip")
	createZip(t, archivePath, map[string]string{
		"subdir/nested.txt": "nested content",
	})

	destDir := filepath.Join(t.TempDir(), "extracted")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("failed to create dest dir: %v", err)
	}

	if err := ExtractZip(archivePath, destDir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(
		filepath.Join(destDir, "subdir", "nested.txt"))
	if err != nil {
		t.Fatalf("failed to read extracted file: %v", err)
	}
	if string(got) != "nested content" {
		t.Errorf("file contents = %q, want %q",
			string(got), "nested content")
	}
}

func TestExtractZipMultipleFiles(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "test.zip")
	createZip(t, archivePath, map[string]string{
		"a.txt": "aaa",
		"b.txt": "bbb",
	})

	destDir := filepath.Join(t.TempDir(), "extracted")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("failed to create dest dir: %v", err)
	}

	if err := ExtractZip(archivePath, destDir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, name := range []string{"a.txt", "b.txt"} {
		if _, err := os.Stat(
			filepath.Join(destDir, name)); err != nil {
			t.Errorf("expected file %q to exist: %v", name, err)
		}
	}
}

// --- Behavior 5: Download error handling ---

func TestFetchReturnsErrorOn404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "not found", http.StatusNotFound)
		}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "output.bin")

	err := Fetch(srv.URL, dest)
	if err == nil {
		t.Fatal("expected error for 404 response")
	}

	_, statErr := os.Stat(dest)
	if !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("dest file should not exist after failed fetch")
	}
}

func TestFetchReturnsErrorOn500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "server error",
				http.StatusInternalServerError)
		}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "output.bin")

	err := Fetch(srv.URL, dest)
	if err == nil {
		t.Fatal("expected error for 500 response")
	}

	_, statErr := os.Stat(dest)
	if !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("dest file should not exist after failed fetch")
	}
}

func TestFetchReturnsErrorForBadURL(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "output.bin")

	err := Fetch("http://127.0.0.1:0/nonexistent", dest)
	if err == nil {
		t.Fatal("expected error for connection failure")
	}
}

// --- Behavior 6: Intermediate directory creation ---

func TestFetchCreatesIntermediateDirectories(t *testing.T) {
	want := "nested content"
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, want)
		}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "a", "b", "file.bin")

	if err := Fetch(srv.URL, dest); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("failed to read dest file: %v", err)
	}
	if string(got) != want {
		t.Errorf("file contents = %q, want %q",
			string(got), want)
	}
}

// --- Security: path traversal rejection ---

func TestExtractTarGzRejectsPathTraversal(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "evil.tar.gz")
	destDir := t.TempDir()

	// Build a tar.gz with a path-traversal entry.
	f, err := os.Create(archive)
	if err != nil {
		t.Fatalf("failed to create archive: %v", err)
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	hdr := &tar.Header{
		Name: "../escape.txt",
		Mode: 0o644,
		Size: 5,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("failed to write header: %v", err)
	}
	if _, err := tw.Write([]byte("owned")); err != nil {
		t.Fatalf("failed to write content: %v", err)
	}
	tw.Close()
	gw.Close()
	f.Close()

	err = ExtractTarGz(archive, destDir)
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
}

func TestExtractTarGzHandlesHardLinks(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "hardlink.tar.gz")
	destDir := t.TempDir()

	// Build a tar.gz with a file and a hard link to it.
	f, err := os.Create(archive)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	content := "hardlink-target-content"
	tw.WriteHeader(&tar.Header{
		Name: "original.txt",
		Mode: 0o644,
		Size: int64(len(content)),
	})
	tw.Write([]byte(content))

	tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeLink,
		Name:     "linked.txt",
		Linkname: "original.txt",
	})

	tw.Close()
	gw.Close()
	f.Close()

	err = ExtractTarGz(archive, destDir)
	if err != nil {
		t.Fatalf("ExtractTarGz error: %v", err)
	}

	// Both files should exist with same content.
	got, err := os.ReadFile(filepath.Join(destDir, "linked.txt"))
	if err != nil {
		t.Fatalf("read hard link: %v", err)
	}
	if string(got) != content {
		t.Errorf("linked.txt = %q, want %q", got, content)
	}
}

func TestExtractTarGzRejectsSymlinkTraversalRelative(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "evil.tar.gz")
	destDir := t.TempDir()

	// Build a tar.gz with a symlink whose target escapes destDir.
	f, err := os.Create(archive)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeSymlink,
		Name:     "escape",
		Linkname: "../../etc/passwd",
	})

	tw.Close()
	gw.Close()
	f.Close()

	err = ExtractTarGz(archive, destDir)
	if err == nil {
		t.Fatal("expected error for symlink traversal")
	}
	if !strings.Contains(err.Error(), "illegal symlink") {
		t.Errorf("error = %q, want it to contain 'illegal symlink'",
			err.Error())
	}
}

func TestExtractTarGzAllowsSymlinkWithinDestDir(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "good.tar.gz")
	destDir := t.TempDir()

	// Build a tar.gz with a valid symlink inside destDir.
	f, err := os.Create(archive)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	content := "target content"
	tw.WriteHeader(&tar.Header{
		Name: "target.txt",
		Mode: 0o644,
		Size: int64(len(content)),
	})
	tw.Write([]byte(content))

	tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeSymlink,
		Name:     "link.txt",
		Linkname: "target.txt",
	})

	tw.Close()
	gw.Close()
	f.Close()

	err = ExtractTarGz(archive, destDir)
	if err != nil {
		t.Fatalf("unexpected error for valid symlink: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(destDir, "link.txt"))
	if err != nil {
		t.Fatalf("read symlink: %v", err)
	}
	if string(got) != content {
		t.Errorf("symlink content = %q, want %q", got, content)
	}
}

// --- Security: safe absolute symlink allowlist ---

func TestExtractTarGzAllowsSymlinkToDevNull(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "devnull.tar.gz")
	destDir := t.TempDir()

	f, err := os.Create(archive)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	content := "regular file"
	tw.WriteHeader(&tar.Header{
		Name: "file.txt",
		Mode: 0o644,
		Size: int64(len(content)),
	})
	tw.Write([]byte(content))

	tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeSymlink,
		Name:     "null",
		Linkname: "/dev/null",
	})

	tw.Close()
	gw.Close()
	f.Close()

	err = ExtractTarGz(archive, destDir)
	if err != nil {
		t.Fatalf("unexpected error for /dev/null symlink: %v", err)
	}
}

func TestExtractTarGzSymlinkToDevNullCreatesCorrectSymlink(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "devnull.tar.gz")
	destDir := t.TempDir()

	f, err := os.Create(archive)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	content := "regular file"
	tw.WriteHeader(&tar.Header{
		Name: "file.txt",
		Mode: 0o644,
		Size: int64(len(content)),
	})
	tw.Write([]byte(content))

	tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeSymlink,
		Name:     "null",
		Linkname: "/dev/null",
	})

	tw.Close()
	gw.Close()
	f.Close()

	if err = ExtractTarGz(archive, destDir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	linkPath := filepath.Join(destDir, "null")
	info, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("symlink not created: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected symlink, got %v", info.Mode())
	}

	target, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if target != "/dev/null" {
		t.Errorf("symlink target = %q, want %q", target, "/dev/null")
	}
}

// TestExtractTarGzAllowsAbsoluteSymlinkToArbitraryPath verifies that
// an absolute symlink pointing to an arbitrary path outside destDir
// is extracted as a (potentially dangling) symlink rather than failing.
func TestExtractTarGzAllowsAbsoluteSymlinkToArbitraryPath(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "arb.tar.gz")
	destDir := t.TempDir()

	f, err := os.Create(archive)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeSymlink,
		Name:     "link",
		Linkname: "/tmp/some/arbitrary/path",
	})

	tw.Close()
	gw.Close()
	f.Close()

	if err := ExtractTarGz(archive, destDir); err != nil {
		t.Fatalf("unexpected error for absolute symlink to arbitrary path: %v", err)
	}

	// Symlink must exist (even though the target doesn't).
	linkPath := filepath.Join(destDir, "link")
	info, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("symlink not created: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected symlink, got %v", info.Mode())
	}
	target, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if target != "/tmp/some/arbitrary/path" {
		t.Errorf("symlink target = %q, want %q", target, "/tmp/some/arbitrary/path")
	}
}

// TestExtractTarGzAllowsAbsoluteSymlinkToDeveloperPath mirrors the helix
// release case where an upstream developer's local path leaked into the
// tarball (e.g. runtime/grammars/sources/move/queries -> /Users/someone/...).
func TestExtractTarGzAllowsAbsoluteSymlinkToDeveloperPath(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "dev.tar.gz")
	destDir := t.TempDir()

	f, err := os.Create(archive)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeSymlink,
		Name:     "queries",
		Linkname: "/Users/someone/code/project/file",
	})

	tw.Close()
	gw.Close()
	f.Close()

	if err := ExtractTarGz(archive, destDir); err != nil {
		t.Fatalf("unexpected error for developer path symlink: %v", err)
	}

	linkPath := filepath.Join(destDir, "queries")
	info, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("symlink not created: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected symlink, got %v", info.Mode())
	}
	target, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if target != "/Users/someone/code/project/file" {
		t.Errorf("symlink target = %q, want %q",
			target, "/Users/someone/code/project/file")
	}
}

// TestExtractTarGzAllowsAbsoluteSymlinkToNonExistent mirrors the helm
// testdata case: invalid-symlink -> /non/existing/file.
func TestExtractTarGzAllowsAbsoluteSymlinkToNonExistent(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "nonex.tar.gz")
	destDir := t.TempDir()

	f, err := os.Create(archive)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeSymlink,
		Name:     "invalid-symlink",
		Linkname: "/non/existing/file",
	})

	tw.Close()
	gw.Close()
	f.Close()

	if err := ExtractTarGz(archive, destDir); err != nil {
		t.Fatalf("unexpected error for symlink to non-existent path: %v", err)
	}

	linkPath := filepath.Join(destDir, "invalid-symlink")
	info, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("symlink not created: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected symlink, got %v", info.Mode())
	}
	target, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if target != "/non/existing/file" {
		t.Errorf("symlink target = %q, want %q", target, "/non/existing/file")
	}
}

func TestExtractTarGzAllowsSymlinkToDevZero(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "devzero.tar.gz")
	destDir := t.TempDir()

	f, err := os.Create(archive)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeSymlink,
		Name:     "zero",
		Linkname: "/dev/zero",
	})

	tw.Close()
	gw.Close()
	f.Close()

	err = ExtractTarGz(archive, destDir)
	if err != nil {
		t.Fatalf("unexpected error for /dev/zero symlink: %v", err)
	}
}

func TestExtractTarGzAllowsBareRootDirEntry(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "root.tar.gz")
	destDir := t.TempDir()

	f, err := os.Create(archive)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	// A bare ./ root entry — common in tarballs.
	tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeDir,
		Name:     "./",
		Mode:     0o755,
	})
	tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     "./hello.txt",
		Mode:     0o644,
		Size:     5,
	})
	tw.Write([]byte("hello"))

	tw.Close()
	gw.Close()
	f.Close()

	err = ExtractTarGz(archive, destDir)
	if err != nil {
		t.Fatalf("unexpected error for bare ./ entry: %v", err)
	}

	// Verify the file was extracted.
	data, err := os.ReadFile(filepath.Join(destDir, "hello.txt"))
	if err != nil {
		t.Fatalf("read extracted file: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("content = %q, want %q", string(data), "hello")
	}
}

func TestExtractZipRejectsPathTraversal(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "evil.zip")
	destDir := t.TempDir()

	// Build a zip with a path-traversal entry.
	f, err := os.Create(archive)
	if err != nil {
		t.Fatalf("failed to create archive: %v", err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("../escape.txt")
	if err != nil {
		t.Fatalf("failed to create entry: %v", err)
	}
	if _, err := w.Write([]byte("owned")); err != nil {
		t.Fatalf("failed to write content: %v", err)
	}
	zw.Close()
	f.Close()

	err = ExtractZip(archive, destDir)
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
}

// --- test helpers ---

// createTarGz builds a tar.gz archive at archivePath containing
// the given files map (relative path -> content). Directory
// entries are emitted for any intermediate paths.
func createTarGz(t *testing.T, archivePath string, files map[string]string) {
	t.Helper()

	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("failed to create archive file: %v", err)
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	writeTarEntries(t, tw, files)
}

// createZip builds a zip archive at archivePath containing
// the given files map (relative path -> content).
func createZip(t *testing.T, archivePath string, files map[string]string) {
	t.Helper()

	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("failed to create archive file: %v", err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	defer zw.Close()

	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("failed to create zip entry: %v", err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("failed to write zip content: %v", err)
		}
	}
}

// createTarZstd builds a tar.zst archive at archivePath containing
// the given files map (relative path -> content). Directory
// entries are emitted for any intermediate paths.
func createTarZstd(t *testing.T, archivePath string, files map[string]string) {
	t.Helper()

	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("failed to create archive file: %v", err)
	}
	defer f.Close()

	zw, err := zstd.NewWriter(f)
	if err != nil {
		t.Fatalf("failed to create zstd writer: %v", err)
	}
	defer zw.Close()

	tw := tar.NewWriter(zw)
	defer tw.Close()

	writeTarEntries(t, tw, files)
}

// createTarXz builds a tar.xz archive at archivePath containing
// the given files map (relative path -> content). Directory
// entries are emitted for any intermediate paths.
func createTarXz(t *testing.T, archivePath string, files map[string]string) {
	t.Helper()

	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("failed to create archive file: %v", err)
	}
	defer f.Close()

	xw, err := xz.NewWriter(f)
	if err != nil {
		t.Fatalf("failed to create xz writer: %v", err)
	}
	defer xw.Close()

	tw := tar.NewWriter(xw)
	defer tw.Close()

	writeTarEntries(t, tw, files)
}

// createTarBz2 builds a tar.bz2 archive at archivePath containing
// the given files map (relative path -> content). It writes a
// plain tar to a temp file, then compresses with the bzip2 command.
func createTarBz2(t *testing.T, archivePath string, files map[string]string) {
	t.Helper()

	// Write uncompressed tar to a temp file.
	tarPath := archivePath + ".tar"
	tf, err := os.Create(tarPath)
	if err != nil {
		t.Fatalf("failed to create tar file: %v", err)
	}

	tw := tar.NewWriter(tf)
	writeTarEntries(t, tw, files)
	tw.Close()
	tf.Close()

	// Compress with bzip2 command.
	cmd := exec.Command("bzip2", "-k", tarPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bzip2 failed: %v\n%s", err, out)
	}

	// bzip2 -k creates tarPath+".bz2" — rename to archivePath.
	if err := os.Rename(tarPath+".bz2", archivePath); err != nil {
		t.Fatalf("rename bz2: %v", err)
	}
	os.Remove(tarPath)
}

// writeTarEntries writes file entries to a tar writer.
// Shared by createTarGz, createTarXz, createTarBz2, etc.
func writeTarEntries(t *testing.T, tw *tar.Writer, files map[string]string) {
	t.Helper()

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	dirs := make(map[string]bool)
	for _, name := range names {
		if dir := filepath.Dir(name); dir != "." {
			parts := strings.Split(
				filepath.ToSlash(dir), "/")
			for i := range parts {
				d := strings.Join(parts[:i+1], "/") + "/"
				if !dirs[d] {
					dirs[d] = true
					dhdr := &tar.Header{
						Typeflag: tar.TypeDir,
						Name:     d,
						Mode:     0o755,
					}
					if err := tw.WriteHeader(dhdr); err != nil {
						t.Fatalf("write dir header: %v", err)
					}
				}
			}
		}

		content := files[name]
		hdr := &tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write tar header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("write tar content: %v", err)
		}
	}
}

// --- Behavior 7: Extract tar.zst ---

func TestExtractTarZstdPreservesFileContents(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "test.tar.zst")
	createTarZstd(t, archivePath, map[string]string{
		"hello.txt": "hello world",
	})

	destDir := filepath.Join(t.TempDir(), "extracted")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("failed to create dest dir: %v", err)
	}

	if err := ExtractTarZstd(archivePath, destDir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(destDir, "hello.txt"))
	if err != nil {
		t.Fatalf("failed to read extracted file: %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("file contents = %q, want %q",
			string(got), "hello world")
	}
}

func TestExtractTarZstdPreservesRelativePaths(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "test.tar.zst")
	createTarZstd(t, archivePath, map[string]string{
		"subdir/nested.txt": "nested content",
	})

	destDir := filepath.Join(t.TempDir(), "extracted")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("failed to create dest dir: %v", err)
	}

	if err := ExtractTarZstd(archivePath, destDir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(
		filepath.Join(destDir, "subdir", "nested.txt"))
	if err != nil {
		t.Fatalf("failed to read extracted file: %v", err)
	}
	if string(got) != "nested content" {
		t.Errorf("file contents = %q, want %q",
			string(got), "nested content")
	}
}

func TestExtractTarZstdMultipleFiles(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "test.tar.zst")
	createTarZstd(t, archivePath, map[string]string{
		"a.txt": "aaa",
		"b.txt": "bbb",
	})

	destDir := filepath.Join(t.TempDir(), "extracted")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("failed to create dest dir: %v", err)
	}

	if err := ExtractTarZstd(archivePath, destDir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, name := range []string{"a.txt", "b.txt"} {
		if _, err := os.Stat(
			filepath.Join(destDir, name)); err != nil {
			t.Errorf("expected file %q to exist: %v", name, err)
		}
	}
}

// --- Behavior 8: Create tar.zst ---

func TestCreateTarZstdRoundTrip(t *testing.T) {
	sourceDir := t.TempDir()

	// Populate source directory with files.
	files := map[string]string{
		"root.txt":        "root content",
		"sub/nested.txt":  "nested content",
		"sub/another.txt": "another file",
	}
	for name, content := range files {
		p := filepath.Join(sourceDir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("failed to create dir: %v", err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("failed to write file: %v", err)
		}
	}

	archivePath := filepath.Join(t.TempDir(), "output.tar.zst")
	if err := CreateTarZstd(sourceDir, archivePath); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the archive exists.
	info, err := os.Stat(archivePath)
	if err != nil {
		t.Fatalf("archive not created: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("archive is empty")
	}

	// Extract and verify round-trip.
	destDir := filepath.Join(t.TempDir(), "extracted")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("failed to create dest dir: %v", err)
	}

	if err := ExtractTarZstd(archivePath, destDir); err != nil {
		t.Fatalf("failed to extract: %v", err)
	}

	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(destDir, name))
		if err != nil {
			t.Errorf("missing file %q: %v", name, err)
			continue
		}
		if string(got) != want {
			t.Errorf("file %q contents = %q, want %q",
				name, string(got), want)
		}
	}
}

func TestCreateTarZstdNoWrapperDirectory(t *testing.T) {
	sourceDir := t.TempDir()

	if err := os.WriteFile(
		filepath.Join(sourceDir, "file.txt"),
		[]byte("data"), 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	archivePath := filepath.Join(t.TempDir(), "output.tar.zst")
	if err := CreateTarZstd(sourceDir, archivePath); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Open the archive and check that entries are relative,
	// with no wrapper directory matching the source dir name.
	f, err := os.Open(archivePath)
	if err != nil {
		t.Fatalf("failed to open archive: %v", err)
	}
	defer f.Close()

	zr, err := zstd.NewReader(f)
	if err != nil {
		t.Fatalf("failed to create zstd reader: %v", err)
	}
	defer zr.Close()

	tr := tar.NewReader(zr)
	sourceName := filepath.Base(sourceDir)
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		if strings.HasPrefix(hdr.Name, sourceName+"/") {
			t.Errorf("entry %q has wrapper directory %q",
				hdr.Name, sourceName)
		}
		if strings.HasPrefix(hdr.Name, "/") {
			t.Errorf("entry %q is absolute, want relative",
				hdr.Name)
		}
	}
}

// --- Behavior: CreateTarZstd does not leak file descriptors ---

func TestCreateTarZstdClosesFilesEagerly(t *testing.T) {
	sourceDir := t.TempDir()

	// Create many files. Without eager closing, file
	// descriptors accumulate in the Walk callback's
	// deferred closures until the outer function returns,
	// which can exhaust the fd limit on systems with
	// low soft limits.
	const fileCount = 500
	for i := 0; i < fileCount; i++ {
		name := fmt.Sprintf("file_%04d.txt", i)
		path := filepath.Join(sourceDir, name)
		if err := os.WriteFile(
			path, []byte("data"), 0o644); err != nil {
			t.Fatalf("write file %d: %v", i, err)
		}
	}

	archivePath := filepath.Join(t.TempDir(), "many.tar.zst")
	if err := CreateTarZstd(sourceDir, archivePath); err != nil {
		t.Fatalf("CreateTarZstd with %d files: %v",
			fileCount, err)
	}

	// Verify round-trip: extract and check file count.
	destDir := filepath.Join(t.TempDir(), "extracted")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("create dest dir: %v", err)
	}
	if err := ExtractTarZstd(archivePath, destDir); err != nil {
		t.Fatalf("extract: %v", err)
	}

	entries, err := os.ReadDir(destDir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != fileCount {
		t.Errorf("extracted %d files, want %d",
			len(entries), fileCount)
	}
}

// --- Security: tar.zst path traversal rejection ---

func TestExtractTarZstdRejectsPathTraversal(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "evil.tar.zst")
	destDir := t.TempDir()

	// Build a tar.zst with a path-traversal entry.
	f, err := os.Create(archive)
	if err != nil {
		t.Fatalf("failed to create archive: %v", err)
	}
	zw, err := zstd.NewWriter(f)
	if err != nil {
		t.Fatalf("failed to create zstd writer: %v", err)
	}
	tw := tar.NewWriter(zw)
	hdr := &tar.Header{
		Name: "../escape.txt",
		Mode: 0o644,
		Size: 5,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("failed to write header: %v", err)
	}
	if _, err := tw.Write([]byte("owned")); err != nil {
		t.Fatalf("failed to write content: %v", err)
	}
	tw.Close()
	zw.Close()
	f.Close()

	err = ExtractTarZstd(archive, destDir)
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
}

// --- Create tar.zst preserves executability ---

func TestCreateTarZstdPreservesExecutability(t *testing.T) {
	sourceDir := t.TempDir()

	// Create a regular file and an executable file.
	if err := os.WriteFile(
		filepath.Join(sourceDir, "normal.txt"),
		[]byte("data"), 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(sourceDir, "run.sh"),
		[]byte("#!/bin/sh"), 0o755); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	archivePath := filepath.Join(t.TempDir(), "output.tar.zst")
	if err := CreateTarZstd(sourceDir, archivePath); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Extract and verify permissions are preserved.
	destDir := filepath.Join(t.TempDir(), "extracted")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("failed to create dest dir: %v", err)
	}

	if err := ExtractTarZstd(archivePath, destDir); err != nil {
		t.Fatalf("failed to extract: %v", err)
	}

	info, err := os.Stat(filepath.Join(destDir, "run.sh"))
	if err != nil {
		t.Fatalf("failed to stat run.sh: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("run.sh should be executable, got mode %v",
			info.Mode())
	}

	info, err = os.Stat(filepath.Join(destDir, "normal.txt"))
	if err != nil {
		t.Fatalf("failed to stat normal.txt: %v", err)
	}
	if info.Mode()&0o111 != 0 {
		t.Errorf("normal.txt should not be executable, got mode %v",
			info.Mode())
	}
}

func TestCreateTarZstdDeterministic(t *testing.T) {
	sourceDir := t.TempDir()

	// Create files.
	binDir := filepath.Join(sourceDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(binDir, "tool"),
		[]byte("#!/bin/sh\necho hello"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a symlink with an absolute path within the tree.
	// This simulates what make install does with ln -s -f.
	absTarget := filepath.Join(binDir, "tool")
	if err := os.Symlink(absTarget,
		filepath.Join(binDir, "tool-alias")); err != nil {
		t.Fatal(err)
	}

	// Create two archives and compare hashes.
	archive1 := filepath.Join(t.TempDir(), "a1.tar.zst")
	archive2 := filepath.Join(t.TempDir(), "a2.tar.zst")

	if err := CreateTarZstd(sourceDir, archive1); err != nil {
		t.Fatalf("first archive: %v", err)
	}
	if err := CreateTarZstd(sourceDir, archive2); err != nil {
		t.Fatalf("second archive: %v", err)
	}

	hash1, err := HashFile(archive1)
	if err != nil {
		t.Fatal(err)
	}
	hash2, err := HashFile(archive2)
	if err != nil {
		t.Fatal(err)
	}
	if hash1 != hash2 {
		t.Errorf("archives differ: %s vs %s", hash1, hash2)
	}

	// Extract and verify symlink target is relative.
	destDir := t.TempDir()
	if err := ExtractTarZstd(archive1, destDir); err != nil {
		t.Fatalf("extract: %v", err)
	}

	link := filepath.Join(destDir, "bin", "tool-alias")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if filepath.IsAbs(target) {
		t.Errorf(
			"symlink target should be relative, got %q",
			target)
	}
}

// --- FetchWithAuth tests ---

func TestFetchWithAuthSendsAuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewTLSServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			fmt.Fprint(w, "content")
		}))
	defer srv.Close()

	restore := SetHTTPClient(srv.Client())
	defer restore()

	dest := filepath.Join(t.TempDir(), "out.bin")
	err := FetchWithAuth(srv.URL+"/blob", dest, "my-token-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotAuth != "Bearer my-token-123" {
		t.Errorf("Authorization = %q, want %q",
			gotAuth, "Bearer my-token-123")
	}
}

func TestFetchWithAuthWritesFile(t *testing.T) {
	want := "binary-content-here"
	srv := httptest.NewTLSServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, want)
		}))
	defer srv.Close()

	restore := SetHTTPClient(srv.Client())
	defer restore()

	dest := filepath.Join(t.TempDir(), "out.bin")
	err := FetchWithAuth(srv.URL+"/blob", dest, "tok")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(got) != want {
		t.Errorf("file content = %q, want %q", got, want)
	}
}

func TestFetchWithAuthRejectsPlainHTTP(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "out.bin")
	err := FetchWithAuth(
		"http://example.com/blob", dest, "my-token")
	if err == nil {
		t.Fatal("expected error for plain HTTP with bearer token")
	}
	if !strings.Contains(err.Error(), "https") {
		t.Errorf("error = %q, want it to mention https",
			err.Error())
	}
}

func TestFetchWithAuthErrorsOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "denied", http.StatusForbidden)
		}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out.bin")
	err := FetchWithAuth(srv.URL+"/blob", dest, "tok")
	if err == nil {
		t.Fatal("expected error for 403 status")
	}
}

// --- Behavior 9: Extract tar.xz ---

func TestExtractTarXzPreservesFileContents(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "test.tar.xz")
	createTarXz(t, archivePath, map[string]string{
		"hello.txt": "hello world",
	})

	destDir := filepath.Join(t.TempDir(), "extracted")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("failed to create dest dir: %v", err)
	}

	if err := ExtractTarXz(archivePath, destDir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(destDir, "hello.txt"))
	if err != nil {
		t.Fatalf("failed to read extracted file: %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("file contents = %q, want %q",
			string(got), "hello world")
	}
}

func TestExtractTarXzPreservesRelativePaths(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "test.tar.xz")
	createTarXz(t, archivePath, map[string]string{
		"subdir/nested.txt": "nested content",
	})

	destDir := filepath.Join(t.TempDir(), "extracted")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("failed to create dest dir: %v", err)
	}

	if err := ExtractTarXz(archivePath, destDir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(
		filepath.Join(destDir, "subdir", "nested.txt"))
	if err != nil {
		t.Fatalf("failed to read extracted file: %v", err)
	}
	if string(got) != "nested content" {
		t.Errorf("file contents = %q, want %q",
			string(got), "nested content")
	}
}

// --- Behavior 10: Extract tar.bz2 ---

func TestExtractTarBz2PreservesFileContents(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "test.tar.bz2")
	createTarBz2(t, archivePath, map[string]string{
		"hello.txt": "hello world",
	})

	destDir := filepath.Join(t.TempDir(), "extracted")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("failed to create dest dir: %v", err)
	}

	if err := ExtractTarBz2(archivePath, destDir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(destDir, "hello.txt"))
	if err != nil {
		t.Fatalf("failed to read extracted file: %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("file contents = %q, want %q",
			string(got), "hello world")
	}
}

func TestExtractTarBz2PreservesRelativePaths(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "test.tar.bz2")
	createTarBz2(t, archivePath, map[string]string{
		"subdir/nested.txt": "nested content",
	})

	destDir := filepath.Join(t.TempDir(), "extracted")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("failed to create dest dir: %v", err)
	}

	if err := ExtractTarBz2(archivePath, destDir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(
		filepath.Join(destDir, "subdir", "nested.txt"))
	if err != nil {
		t.Fatalf("failed to read extracted file: %v", err)
	}
	if string(got) != "nested content" {
		t.Errorf("file contents = %q, want %q",
			string(got), "nested content")
	}
}

// --- Behavior 11: ExtractSource dispatcher ---

func TestExtractSourceDetectsFormat(t *testing.T) {
	files := map[string]string{"data.txt": "content"}

	tests := []struct {
		name   string
		ext    string
		create func(t *testing.T, path string, files map[string]string)
	}{
		{"tar.gz", ".tar.gz", createTarGz},
		{"tgz", ".tgz", createTarGz},
		{"tar.xz", ".tar.xz", createTarXz},
		{"tar.bz2", ".tar.bz2", createTarBz2},
		{"tar.zst", ".tar.zst", createTarZstd},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			archivePath := filepath.Join(
				t.TempDir(), "archive"+tt.ext)
			tt.create(t, archivePath, files)

			destDir := filepath.Join(t.TempDir(), "extracted")
			if err := os.MkdirAll(destDir, 0o755); err != nil {
				t.Fatalf("create dest dir: %v", err)
			}

			if err := ExtractSource(archivePath, destDir); err != nil {
				t.Fatalf("ExtractSource(%s): %v", tt.ext, err)
			}

			got, err := os.ReadFile(
				filepath.Join(destDir, "data.txt"))
			if err != nil {
				t.Fatalf("read extracted file: %v", err)
			}
			if string(got) != "content" {
				t.Errorf("contents = %q, want %q",
					string(got), "content")
			}
		})
	}
}

func TestExtractSourceRejectsUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.tar.lz4")
	if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	err := ExtractSource(path, t.TempDir())
	if err == nil {
		t.Fatal("expected error for unsupported format")
	}
	if !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("error = %q, want it to contain 'unsupported'",
			err.Error())
	}
}
