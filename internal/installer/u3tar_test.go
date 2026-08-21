package installer

import (
	"archive/tar"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// u3TarEntry is one ordered entry for createU3TarZstd.
type u3TarEntry struct {
	name    string
	content string
	mode    int64
	// link makes this entry a symlink to the given target instead of
	// a regular file.
	link string
}

func createU3TarZstd(t *testing.T, entries []u3TarEntry) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pkg.tar.zst")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create tar.zst: %v", err)
	}
	zw, err := zstd.NewWriter(f)
	if err != nil {
		t.Fatalf("create zstd writer: %v", err)
	}
	tw := tar.NewWriter(zw)
	for _, e := range entries {
		if e.link != "" {
			if err := tw.WriteHeader(&tar.Header{
				Typeflag: tar.TypeSymlink,
				Name:     e.name,
				Linkname: e.link,
				Mode:     0o777,
			}); err != nil {
				t.Fatalf("write symlink header: %v", err)
			}
			continue
		}
		if err := tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg,
			Name:     e.name,
			Mode:     e.mode,
			Size:     int64(len(e.content)),
		}); err != nil {
			t.Fatalf("write tar header: %v", err)
		}
		if _, err := tw.Write([]byte(e.content)); err != nil {
			t.Fatalf("write tar content: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zstd writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}
	return path
}
