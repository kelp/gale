package download

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ulikunitz/xz"
)

// Behavior 0 (HTTP client has a whole-transfer timeout) was
// removed by gh#61: the 5-minute client Timeout aborted large
// transfers mid-stream. The replacement invariant — the shared
// no-timeout client from internal/httpclient — is asserted in
// audit_fix_U12_test.go.

// --- Behavior 1: Download file from URL ---

func TestFetchWritesFileToDestPath(t *testing.T) {
	want := "hello from the server"
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, want)
		},
	))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "downloaded.txt")

	if err := Fetch(context.Background(), srv.URL, dest); err != nil {
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

	if err := VerifySHA256(context.Background(), path, expected); err != nil {
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
	err := VerifySHA256(context.Background(), path, wrongHash)
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
	err := VerifySHA256(context.Background(), path, wrongHash)
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
	err := VerifySHA256(context.Background(), "/nonexistent/path/file.txt", "abc123")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

// extractCases is the format matrix used by the three
// table-driven extract behavior tests below.
var extractCases = []struct {
	name    string
	ext     string
	create  func(*testing.T, string, map[string]string)
	extract func(context.Context, string, string) error
}{
	{"tar.gz", ".tar.gz", createTarGz, ExtractTarGz},
	{"zip", ".zip", createZip, ExtractZip},
	{"tar.xz", ".tar.xz", createTarXz, ExtractTarXz},
}

// --- Behaviors 3/4/7/9/10: extract across all formats ---

func TestExtractPreservesFileContents(t *testing.T) {
	for _, tc := range extractCases {
		t.Run(tc.name, func(t *testing.T) {
			archivePath := filepath.Join(
				t.TempDir(), "test"+tc.ext,
			)
			tc.create(t, archivePath, map[string]string{
				"hello.txt": "hello world",
			})

			destDir := filepath.Join(t.TempDir(), "extracted")
			if err := os.MkdirAll(destDir, 0o755); err != nil {
				t.Fatalf("create dest dir: %v", err)
			}
			if err := tc.extract(context.Background(), archivePath, destDir); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			got, err := os.ReadFile(
				filepath.Join(destDir, "hello.txt"),
			)
			if err != nil {
				t.Fatalf("read extracted file: %v", err)
			}
			if string(got) != "hello world" {
				t.Errorf("file contents = %q, want %q",
					string(got), "hello world")
			}
		})
	}
}

func TestExtractPreservesRelativePaths(t *testing.T) {
	for _, tc := range extractCases {
		t.Run(tc.name, func(t *testing.T) {
			archivePath := filepath.Join(
				t.TempDir(), "test"+tc.ext,
			)
			tc.create(t, archivePath, map[string]string{
				"subdir/nested.txt": "nested content",
			})

			destDir := filepath.Join(t.TempDir(), "extracted")
			if err := os.MkdirAll(destDir, 0o755); err != nil {
				t.Fatalf("create dest dir: %v", err)
			}
			if err := tc.extract(context.Background(), archivePath, destDir); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			got, err := os.ReadFile(
				filepath.Join(destDir, "subdir", "nested.txt"),
			)
			if err != nil {
				t.Fatalf("read extracted file: %v", err)
			}
			if string(got) != "nested content" {
				t.Errorf("file contents = %q, want %q",
					string(got), "nested content")
			}
		})
	}
}

func TestExtractMultipleFiles(t *testing.T) {
	for _, tc := range extractCases {
		t.Run(tc.name, func(t *testing.T) {
			archivePath := filepath.Join(
				t.TempDir(), "test"+tc.ext,
			)
			tc.create(t, archivePath, map[string]string{
				"a.txt": "aaa",
				"b.txt": "bbb",
			})

			destDir := filepath.Join(t.TempDir(), "extracted")
			if err := os.MkdirAll(destDir, 0o755); err != nil {
				t.Fatalf("create dest dir: %v", err)
			}
			if err := tc.extract(context.Background(), archivePath, destDir); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			for _, name := range []string{"a.txt", "b.txt"} {
				if _, err := os.Stat(
					filepath.Join(destDir, name),
				); err != nil {
					t.Errorf("expected file %q to exist: %v",
						name, err)
				}
			}
		})
	}
}

// --- Behavior 5: Download error handling ---

func TestFetchReturnsErrorOn404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "not found", http.StatusNotFound)
		},
	))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "output.bin")

	err := Fetch(context.Background(), srv.URL, dest)
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
		},
	))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "output.bin")

	err := Fetch(context.Background(), srv.URL, dest)
	if err == nil {
		t.Fatal("expected error for 500 response")
	}

	_, statErr := os.Stat(dest)
	if !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("dest file should not exist after failed fetch")
	}
}

func TestFetchNoFallbackForNonMirroredURL(t *testing.T) {
	// Non-mirrored URL should fail normally, no fallback.
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "forbidden", http.StatusForbidden)
		},
	))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "output.bin")
	err := Fetch(context.Background(), srv.URL+"/some/file.tar.gz", dest)
	if err == nil {
		t.Fatal("expected error for 403 with no mirrors")
	}
}

func TestFetchReturnsErrorForBadURL(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "output.bin")

	err := Fetch(context.Background(), "http://127.0.0.1:0/nonexistent", dest)
	if err == nil {
		t.Fatal("expected error for connection failure")
	}
}

func TestProgressWriterDisabledWriteEmitsNothing(t *testing.T) {
	restore := SetProgressEnabled(false)
	defer restore()

	out := captureStderr(t, func() {
		pw := &progressWriter{
			total: 1024,
			start: time.Now().Add(-time.Second),
			name:  "test.tar.gz",
		}
		if _, err := pw.Write([]byte(strings.Repeat("a", 512))); err != nil {
			t.Fatalf("write: %v", err)
		}
	})

	if out != "" {
		t.Errorf("stderr = %q, want empty when progress is disabled", out)
	}
}

func TestProgressWriterDisabledFinishEmitsNothing(t *testing.T) {
	restore := SetProgressEnabled(false)
	defer restore()

	out := captureStderr(t, func() {
		pw := &progressWriter{
			written: 1024,
			start:   time.Now().Add(-time.Second),
			name:    "test.tar.gz",
		}
		pw.finish()
	})

	if out != "" {
		t.Errorf("stderr = %q, want empty when progress is disabled", out)
	}
}

func TestProgressWriterEnabledWriteEmitsProgressLine(t *testing.T) {
	restore := SetProgressEnabled(true)
	defer restore()

	out := captureStderr(t, func() {
		pw := &progressWriter{
			total: 1024,
			start: time.Now().Add(-time.Second),
			name:  "test.tar.gz",
		}
		if _, err := pw.Write([]byte(strings.Repeat("a", 512))); err != nil {
			t.Fatalf("write: %v", err)
		}
	})

	if !strings.Contains(out, "Downloading - test.tar.gz") {
		t.Errorf("stderr = %q, want download progress line", out)
	}
	if !strings.Contains(out, "\r") {
		t.Errorf("stderr = %q, want carriage return progress output", out)
	}
}

// --- Behavior 6: Intermediate directory creation ---

func TestFetchCreatesIntermediateDirectories(t *testing.T) {
	want := "nested content"
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, want)
		},
	))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "a", "b", "file.bin")

	if err := Fetch(context.Background(), srv.URL, dest); err != nil {
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

	err = ExtractTarGz(context.Background(), archive, destDir)
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
}

func TestExtractTarGzHandlesHardLinks(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "hardlink.tar.gz")
	destDir := t.TempDir()
	content := "hardlink-target-content"
	writeTarGzHeaders(t, archive, []tar.Header{
		{Name: "original.txt", Mode: 0o644},
		{Typeflag: tar.TypeLink, Name: "linked.txt", Linkname: "original.txt"},
	}, []string{content, ""})

	err := ExtractTarGz(context.Background(), archive, destDir)
	if err != nil {
		t.Fatalf("ExtractTarGz error: %v", err)
	}

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

	err = ExtractTarGz(context.Background(), archive, destDir)
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
	content := "target content"
	writeTarGzHeaders(t, archive, []tar.Header{
		{Name: "target.txt", Mode: 0o644},
		{Typeflag: tar.TypeSymlink, Name: "link.txt", Linkname: "target.txt"},
	}, []string{content, ""})

	err := ExtractTarGz(context.Background(), archive, destDir)
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

// TestExtractTarGzAbsoluteSymlinks verifies that absolute symlinks
// with various targets (device nodes, developer paths, non-existent
// paths) are extracted as symlinks rather than rejected. Each case
// checks: no error on extract AND the symlink exists with the right
// target.
func TestExtractTarGzAbsoluteSymlinks(t *testing.T) {
	cases := []struct {
		name     string // archive entry name
		linkname string // symlink target
	}{
		{
			name:     "null",
			linkname: "/dev/null",
		},
		{
			name:     "zero",
			linkname: "/dev/zero",
		},
		{
			name:     "link",
			linkname: "/tmp/some/arbitrary/path",
		},
		{
			name: "queries",
			// mirrors the helix release case where an upstream
			// developer's local path leaked into the tarball
			linkname: "/Users/someone/code/project/file",
		},
		{
			name: "invalid-symlink",
			// mirrors the helm testdata case
			linkname: "/non/existing/file",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name+":"+tc.linkname, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "abs.tar.gz")
			destDir := t.TempDir()

			f, err := os.Create(archive)
			if err != nil {
				t.Fatalf("create archive: %v", err)
			}
			gw := gzip.NewWriter(f)
			tw := tar.NewWriter(gw)

			if err := tw.WriteHeader(&tar.Header{
				Typeflag: tar.TypeSymlink,
				Name:     tc.name,
				Linkname: tc.linkname,
			}); err != nil {
				t.Fatalf("write symlink header: %v", err)
			}

			tw.Close()
			gw.Close()
			f.Close()

			if err := ExtractTarGz(context.Background(), archive, destDir); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			linkPath := filepath.Join(destDir, tc.name)
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
			if target != tc.linkname {
				t.Errorf("symlink target = %q, want %q",
					target, tc.linkname)
			}
		})
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

	err = ExtractTarGz(context.Background(), archive, destDir)
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

	err = ExtractZip(context.Background(), archive, destDir)
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
				filepath.ToSlash(dir), "/",
			)
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			archivePath := filepath.Join(
				t.TempDir(), "archive"+tt.ext,
			)
			tt.create(t, archivePath, files)

			destDir := filepath.Join(t.TempDir(), "extracted")
			if err := os.MkdirAll(destDir, 0o755); err != nil {
				t.Fatalf("create dest dir: %v", err)
			}

			if err := ExtractSource(context.Background(), archivePath, destDir); err != nil {
				t.Fatalf("ExtractSource(context.Background(), %s): %v", tt.ext, err)
			}

			got, err := os.ReadFile(
				filepath.Join(destDir, "data.txt"),
			)
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

func TestExtractSourceRejectsZstdAndBz2(t *testing.T) {
	for _, ext := range []string{".tar.zst", ".tar.bz2"} {
		t.Run(ext, func(t *testing.T) {
			archivePath := filepath.Join(t.TempDir(), "archive"+ext)
			if err := os.WriteFile(archivePath, []byte("not-an-archive"), 0o644); err != nil {
				t.Fatal(err)
			}
			err := ExtractSource(context.Background(), archivePath, t.TempDir())
			if err == nil {
				t.Fatal("ExtractSource accepted a deleted bottle format")
			}
			if !strings.Contains(err.Error(), "unsupported") {
				t.Errorf("error = %q, want unsupported", err)
			}
		})
	}
}

func TestExtractSourceRejectsUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.tar.lz4")
	if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	err := ExtractSource(context.Background(), path, t.TempDir())
	if err == nil {
		t.Fatal("expected error for unsupported format")
	}
	if !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("error = %q, want it to contain 'unsupported'",
			err.Error())
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	fn()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	buf, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(buf)
}
