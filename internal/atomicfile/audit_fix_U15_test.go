package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWriteNewFileIsWorldReadable pins F13: a brand-new file written
// by Write must be 0644 (world-readable), not 0600 (os.CreateTemp
// default). This matches the project convention for config and lock
// files and prevents spurious mode-change diffs in git.
func TestWriteNewFileIsWorldReadable(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "new.toml")

	if err := Write(path, []byte("new content")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	got := info.Mode().Perm()
	want := os.FileMode(0o644)
	if got != want {
		t.Errorf("new file mode: got %04o, want %04o", got, want)
	}
}

// TestWritePreservesExistingFileMode pins F13: Write must not
// silently downgrade an existing file's permissions to 0600.
// A file created at 0644 must remain 0644 after Write rewrites it.
func TestWritePreservesExistingFileMode(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.toml")

	// Create the file with world-readable permissions (like gale init does).
	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Rewrite with atomicfile.Write.
	if err := Write(path, []byte("updated")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	got := info.Mode().Perm()
	want := os.FileMode(0o644)
	if got != want {
		t.Errorf("mode after Write: got %04o, want %04o", got, want)
	}
}

// TestWritePreservesExecutableBit ensures that an executable file
// (e.g. a script managed via atomicfile) keeps its executable bit
// after rewrite.
func TestWritePreservesExecutableBit(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "script.sh")

	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := Write(path, []byte("#!/bin/sh\n# updated\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	got := info.Mode().Perm()
	want := os.FileMode(0o755)
	if got != want {
		t.Errorf("mode after Write: got %04o, want %04o", got, want)
	}
}
