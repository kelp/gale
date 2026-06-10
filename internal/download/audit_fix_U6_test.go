package download

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

// writeEvilArchive builds a tar.gz from the supplied headers, writing
// raw content for any entry that carries a non-empty body string.
func writeEvilArchive(t *testing.T, path string, entries []struct {
	hdr  tar.Header
	body string
},
) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	for _, e := range entries {
		h := e.hdr
		if e.body != "" {
			h.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatalf("write header %s: %v", h.Name, err)
		}
		if e.body != "" {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatalf("write body %s: %v", h.Name, err)
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

// TestExtractTarGzAbsoluteSymlinkParentEscape captures issue #40: an
// absolute symlink entry followed by a regular-file entry whose path
// traverses that symlink must never write outside destDir.
func TestExtractTarGzAbsoluteSymlinkParentEscape(t *testing.T) {
	base := t.TempDir()
	destDir := filepath.Join(base, "dest")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(base, "evil.tar.gz")
	writeEvilArchive(t, archive, []struct {
		hdr  tar.Header
		body string
	}{
		{hdr: tar.Header{Typeflag: tar.TypeSymlink, Name: "escape", Linkname: outside}},
		{hdr: tar.Header{Typeflag: tar.TypeReg, Name: "escape/pwned", Mode: 0o644}, body: "ESCAPED"},
	})

	// Extraction may legitimately fail; what must never happen is a
	// write landing outside destDir.
	_ = ExtractTarGz(archive, destDir)

	pwned := filepath.Join(outside, "pwned")
	if _, err := os.Stat(pwned); err == nil {
		t.Fatalf("sandbox escape: file written outside destDir at %s", pwned)
	}
}

// TestExtractTarGzAbsoluteSymlinkOverwriteEscape captures the variant
// where a regular-file entry shares a name with a previously extracted
// absolute symlink, so writing the file would follow the symlink and
// clobber the out-of-sandbox target.
func TestExtractTarGzAbsoluteSymlinkOverwriteEscape(t *testing.T) {
	base := t.TempDir()
	destDir := filepath.Join(base, "dest")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(base, "secret.txt")
	if err := os.WriteFile(secret, []byte("ORIGINAL"), 0o644); err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(base, "evil.tar.gz")
	writeEvilArchive(t, archive, []struct {
		hdr  tar.Header
		body string
	}{
		{hdr: tar.Header{Typeflag: tar.TypeSymlink, Name: "x", Linkname: secret}},
		{hdr: tar.Header{Typeflag: tar.TypeReg, Name: "x", Mode: 0o644}, body: "CLOBBERED"},
	})

	_ = ExtractTarGz(archive, destDir)

	got, err := os.ReadFile(secret)
	if err != nil {
		t.Fatalf("read secret: %v", err)
	}
	if string(got) != "ORIGINAL" {
		t.Fatalf("sandbox escape: out-of-sandbox file overwritten, content = %q", got)
	}
}

// TestExtractTarGzAbsoluteSymlinkHardlinkEscape captures the hard-link
// variant: a hard-link entry whose target traverses an absolute symlink
// directory must not pull an out-of-sandbox file into the store.
func TestExtractTarGzAbsoluteSymlinkHardlinkEscape(t *testing.T) {
	base := t.TempDir()
	destDir := filepath.Join(base, "dest")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, []byte("SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(base, "evil.tar.gz")
	writeEvilArchive(t, archive, []struct {
		hdr  tar.Header
		body string
	}{
		{hdr: tar.Header{Typeflag: tar.TypeSymlink, Name: "dir", Linkname: outside}},
		{hdr: tar.Header{Typeflag: tar.TypeLink, Name: "captured", Linkname: "dir/secret"}},
	})

	_ = ExtractTarGz(archive, destDir)

	captured := filepath.Join(destDir, "captured")
	if _, err := os.Lstat(captured); err == nil {
		t.Fatalf("sandbox escape: out-of-sandbox file hardlinked into store at %s", captured)
	}
}
