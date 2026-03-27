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
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

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

	// Collect and sort names for deterministic output.
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	dirs := make(map[string]bool)
	for _, name := range names {
		// Emit directory entries for each ancestor path.
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
						t.Fatalf("failed to write dir header: %v",
							err)
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
			t.Fatalf("failed to write tar header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("failed to write tar content: %v", err)
		}
	}
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

	// Collect and sort names for deterministic output.
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	dirs := make(map[string]bool)
	for _, name := range names {
		// Emit directory entries for each ancestor path.
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
						t.Fatalf("failed to write dir header: %v",
							err)
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
			t.Fatalf("failed to write tar header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("failed to write tar content: %v", err)
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

// --- FetchWithAuth tests ---

func TestFetchWithAuthSendsAuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			fmt.Fprint(w, "content")
		}))
	defer srv.Close()

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
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, want)
		}))
	defer srv.Close()

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
