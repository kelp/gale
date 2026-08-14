package recipemeta

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadMissingReturnsEmpty(t *testing.T) {
	md, err := Read(t.TempDir())
	if err != nil {
		t.Fatalf("Read missing: %v", err)
	}
	if md.Digest != "" {
		t.Errorf("Digest = %q, want empty", md.Digest)
	}
	if Has(t.TempDir()) {
		t.Error("Has = true for a directory with no sidecar")
	}
}

func TestWriteReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := Metadata{Digest: "abc123"}
	if err := Write(dir, want); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !Has(dir) {
		t.Fatal("Has = false after Write")
	}
	got, err := Read(dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got != want {
		t.Errorf("Read = %+v, want %+v", got, want)
	}
	if _, err := os.Stat(filepath.Join(dir, File)); err != nil {
		t.Errorf("sidecar missing: %v", err)
	}
}
