package download

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// umaskMu serializes tests that change the process umask.
var umaskMu sync.Mutex

func withExtractLimits(t *testing.T, entries int, decompressed, compressed int64) {
	t.Helper()
	oldE, oldD, oldC := maxArchiveEntries, maxDecompressedBytes, maxCompressedBytes
	maxArchiveEntries = entries
	maxDecompressedBytes = decompressed
	maxCompressedBytes = compressed
	t.Cleanup(func() {
		maxArchiveEntries = oldE
		maxDecompressedBytes = oldD
		maxCompressedBytes = oldC
	})
}

func writeTarGzHeaders(t *testing.T, archivePath string, headers []tar.Header, bodies []string) {
	t.Helper()
	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	for i, h := range headers {
		hdr := h
		if i < len(bodies) && bodies[i] != "" {
			hdr.Size = int64(len(bodies[i]))
		}
		if err := tw.WriteHeader(&hdr); err != nil {
			t.Fatalf("write header %s: %v", hdr.Name, err)
		}
		if i < len(bodies) && bodies[i] != "" {
			if _, err := tw.Write([]byte(bodies[i])); err != nil {
				t.Fatalf("write body %s: %v", hdr.Name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close file: %v", err)
	}
}

func writeZipFile(t *testing.T, archivePath string, hdr zip.FileHeader, body string) {
	t.Helper()
	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.CreateHeader(&hdr)
	if err != nil {
		t.Fatalf("create zip header: %v", err)
	}
	if _, err := io.WriteString(w, body); err != nil {
		t.Fatalf("write zip body: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close file: %v", err)
	}
}

func filePerm(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	return fi.Mode()
}

func TestExtractMasksSpecialModeBits(t *testing.T) {
	special := os.ModeSetuid | os.ModeSetgid | os.ModeSticky
	cases := []struct {
		name string
		mode int64
	}{
		{"setuid", 0o4755},
		{"setgid", 0o2755},
		{"sticky", 0o1755},
		{"all", 0o7755},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "special.tar.gz")
			writeTarGzHeaders(t, archive, []tar.Header{{
				Name: "tool",
				Mode: tc.mode,
			}}, []string{"hello"})

			dest := t.TempDir()
			if err := ExtractTarGz(context.Background(), archive, dest); err != nil {
				t.Fatalf("ExtractTarGz: %v", err)
			}
			got := filePerm(t, filepath.Join(dest, "tool"))
			if got&special != 0 {
				t.Errorf("mode = %o, want special bits masked", got)
			}
			if got.Perm() != 0o755 {
				t.Errorf("perm = %o, want 0755", got.Perm())
			}
			body, err := os.ReadFile(filepath.Join(dest, "tool"))
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if string(body) != "hello" {
				t.Errorf("body = %q, want %q", body, "hello")
			}
		})
	}
}

func TestExtractPreservesModeAcrossUmask(t *testing.T) {
	umaskMu.Lock()
	defer umaskMu.Unlock()
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)

	t.Run("tar.gz", func(t *testing.T) {
		archive := filepath.Join(t.TempDir(), "mode.tar.gz")
		writeTarGzHeaders(t, archive, []tar.Header{{
			Name: "bin/tool",
			Mode: 0o755,
		}}, []string{"x"})
		dest := t.TempDir()
		if err := ExtractTarGz(context.Background(), archive, dest); err != nil {
			t.Fatalf("ExtractTarGz: %v", err)
		}
		got := filePerm(t, filepath.Join(dest, "bin", "tool")).Perm()
		if got != 0o755 {
			t.Errorf("perm = %o, want 0755", got)
		}
	})

	t.Run("zip", func(t *testing.T) {
		archive := filepath.Join(t.TempDir(), "mode.zip")
		hdr := zip.FileHeader{Name: "bin/tool", Method: zip.Deflate}
		hdr.SetMode(0o755)
		writeZipFile(t, archive, hdr, "x")
		dest := t.TempDir()
		if err := ExtractZip(context.Background(), archive, dest); err != nil {
			t.Fatalf("ExtractZip: %v", err)
		}
		got := filePerm(t, filepath.Join(dest, "bin", "tool")).Perm()
		if got != 0o755 {
			t.Errorf("perm = %o, want 0755", got)
		}
	})
}

func TestExtractRefusesOverEntryCap(t *testing.T) {
	withExtractLimits(t, 2, defaultMaxDecompressedBytes, defaultMaxCompressedBytes)

	archive := filepath.Join(t.TempDir(), "many.tar.gz")
	createTarGz(t, archive, map[string]string{
		"a.txt": "a",
		"b.txt": "b",
		"c.txt": "c",
	})
	err := ExtractTarGz(context.Background(), archive, t.TempDir())
	if !errors.Is(err, ErrExtractLimit) {
		t.Fatalf("error = %v, want ErrExtractLimit", err)
	}
}

func TestExtractAllowsExactEntryCap(t *testing.T) {
	withExtractLimits(t, 2, defaultMaxDecompressedBytes, defaultMaxCompressedBytes)

	archive := filepath.Join(t.TempDir(), "two.tar.gz")
	createTarGz(t, archive, map[string]string{
		"a.txt": "a",
		"b.txt": "b",
	})
	if err := ExtractTarGz(context.Background(), archive, t.TempDir()); err != nil {
		t.Fatalf("ExtractTarGz: %v", err)
	}
}

func TestExtractRefusesOverDecompressedCap(t *testing.T) {
	// 5-byte body plus tar headers is well over 3 copied bytes.
	withExtractLimits(t, defaultMaxArchiveEntries, 3, defaultMaxCompressedBytes)

	archive := filepath.Join(t.TempDir(), "big.tar.gz")
	createTarGz(t, archive, map[string]string{"a.txt": "hello"})
	err := ExtractTarGz(context.Background(), archive, t.TempDir())
	if !errors.Is(err, ErrExtractLimit) {
		t.Fatalf("error = %v, want ErrExtractLimit", err)
	}
}

func TestExtractCountsCopiedBytesNotZipHeader(t *testing.T) {
	// A 5-byte file must refuse a 3-byte decompressed cap even if
	// a lying UncompressedSize64 would have claimed it was smaller.
	withExtractLimits(t, defaultMaxArchiveEntries, 3, defaultMaxCompressedBytes)

	archive := filepath.Join(t.TempDir(), "lie.zip")
	hdr := zip.FileHeader{
		Name:               "a.txt",
		Method:             zip.Store,
		UncompressedSize64: 1,
	}
	hdr.SetMode(0o644)
	writeZipFile(t, archive, hdr, "hello")

	err := ExtractZip(context.Background(), archive, t.TempDir())
	if !errors.Is(err, ErrExtractLimit) {
		t.Fatalf("error = %v, want ErrExtractLimit (copied bytes, not header)", err)
	}
}

func TestExtractZipRefusesNestedTraversal(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "evil.zip")
	hdr := zip.FileHeader{Name: "foo/../../escape.txt", Method: zip.Store}
	hdr.SetMode(0o644)
	writeZipFile(t, archive, hdr, "owned")

	dest := t.TempDir()
	err := ExtractZip(context.Background(), archive, dest)
	if err == nil {
		t.Fatal("expected error for nested path traversal")
	}
	if _, statErr := os.Stat(filepath.Join(dest, "..", "escape.txt")); statErr == nil {
		t.Fatal("wrote outside destDir")
	}
}

func TestExtractZipRefusesSymlinkEntry(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "link.zip")
	hdr := zip.FileHeader{Name: "link", Method: zip.Store}
	hdr.SetMode(os.ModeSymlink | 0o777)
	writeZipFile(t, archive, hdr, "target.txt")

	dest := t.TempDir()
	err := ExtractZip(context.Background(), archive, dest)
	if err == nil {
		t.Fatal("expected error for zip symlink entry")
	}
	path := filepath.Join(dest, "link")
	if fi, statErr := os.Lstat(path); statErr == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			t.Fatal("created a real symlink from a zip entry")
		}
	}
}

func TestExtractRefusesWriteThroughExistingSymlinkParent(t *testing.T) {
	base := t.TempDir()
	dest := filepath.Join(base, "dest")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dest, "escape")); err != nil {
		t.Fatal(err)
	}

	t.Run("tar.gz", func(t *testing.T) {
		archive := filepath.Join(base, "through.tar.gz")
		writeTarGzHeaders(t, archive, []tar.Header{{
			Name: "escape/pwned",
			Mode: 0o644,
		}}, []string{"ESCAPED"})
		_ = ExtractTarGz(context.Background(), archive, dest)
		if _, err := os.Stat(filepath.Join(outside, "pwned")); err == nil {
			t.Fatal("wrote through symlink parent")
		}
	})

	t.Run("zip", func(t *testing.T) {
		archive := filepath.Join(base, "through.zip")
		hdr := zip.FileHeader{Name: "escape/pwned", Method: zip.Store}
		hdr.SetMode(0o644)
		writeZipFile(t, archive, hdr, "ESCAPED")
		_ = ExtractZip(context.Background(), archive, dest)
		if _, err := os.Stat(filepath.Join(outside, "pwned")); err == nil {
			t.Fatal("wrote through symlink parent")
		}
	})
}

func TestExtractTarGzRefusesDeviceAndFIFO(t *testing.T) {
	cases := []struct {
		name     string
		typeflag byte
	}{
		{"fifo", tar.TypeFifo},
		{"char", tar.TypeChar},
		{"block", tar.TypeBlock},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), tc.name+".tar.gz")
			writeTarGzHeaders(t, archive, []tar.Header{{
				Name:     tc.name,
				Typeflag: tc.typeflag,
				Mode:     0o644,
			}}, nil)
			err := ExtractTarGz(context.Background(), archive, t.TempDir())
			if err == nil {
				t.Fatal("expected error for special node")
			}
			if !strings.Contains(err.Error(), "unsupported tar entry type") {
				t.Errorf("error = %q, want it to name the entry type", err)
			}
		})
	}
}

func TestFetchRefusesOverCompressedCap(t *testing.T) {
	restore := SetProgressEnabled(false)
	defer restore()
	withExtractLimits(t, defaultMaxArchiveEntries, defaultMaxDecompressedBytes, 16)

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("0123456789abcdefMORE"))
		},
	))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out")
	err := Fetch(context.Background(), srv.URL, dest)
	if !errors.Is(err, ErrExtractLimit) {
		t.Fatalf("error = %v, want ErrExtractLimit", err)
	}
}
