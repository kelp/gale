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

// An archive can plant .gale-recipe.toml as an absolute symlink.
// Write must unlink that entry and create a regular file; following
// it would clobber a path outside the store.
func TestWriteDoesNotFollowSymlink(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(outside, []byte("original"), 0o644); err != nil {
		t.Fatalf("create victim: %v", err)
	}
	dir := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, File)); err != nil {
		t.Fatalf("plant symlink: %v", err)
	}
	if err := Write(dir, Metadata{Digest: "abc123"}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	victim, err := os.ReadFile(outside)
	if err != nil {
		t.Fatalf("read victim: %v", err)
	}
	if string(victim) != "original" {
		t.Errorf("wrote through the symlink: victim is now %q", victim)
	}
	fi, err := os.Lstat(filepath.Join(dir, File))
	if err != nil {
		t.Fatalf("lstat sidecar: %v", err)
	}
	if !fi.Mode().IsRegular() {
		t.Errorf("sidecar is %v, want a regular file", fi.Mode())
	}
	got, err := Read(dir)
	if err != nil {
		t.Fatalf("Read after Write: %v", err)
	}
	if got.Digest != "abc123" {
		t.Errorf("Digest = %q, want abc123", got.Digest)
	}
}

// A planted symlink is not a recipe record. Read must refuse it
// rather than follow: workingTreeRecipeStale treats a Read error
// as a cache miss, which is the safe answer.
func TestReadRefusesSymlink(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "planted.toml")
	if err := os.WriteFile(outside, []byte("digest = \"planted\"\n"), 0o644); err != nil {
		t.Fatalf("create planted target: %v", err)
	}
	dir := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, File)); err != nil {
		t.Fatalf("plant symlink: %v", err)
	}
	md, err := Read(dir)
	if err == nil {
		t.Fatalf("Read followed a symlink: Digest = %q", md.Digest)
	}
	if md.Digest != "" {
		t.Errorf("Digest = %q on error, want empty", md.Digest)
	}
}
