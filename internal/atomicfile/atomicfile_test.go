package atomicfile

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// seedLink puts a symlink at dir/link pointing at dir/target, and
// writes target with data first when data is non-nil. It returns the
// absolute link and target paths.
func seedLink(
	t *testing.T, dir, link, target string, data []byte,
) (string, string) {
	t.Helper()
	linkPath := filepath.Join(dir, link)
	targetPath := filepath.Join(dir, target)
	if data != nil {
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			t.Fatalf("setup: %v", err)
		}
		if err := os.WriteFile(targetPath, data, 0o644); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	if err := os.Symlink(targetPath, linkPath); err != nil {
		t.Fatalf("setup: %v", err)
	}
	return linkPath, targetPath
}

func TestWriteCreatesNewFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.txt")
	data := []byte("hello world")

	err := Write(path, data)
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	if !bytes.Equal(got, data) {
		t.Errorf("content mismatch: got %q, want %q", got, data)
	}
}

func TestWriteOverwritesExisting(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.txt")

	oldData := []byte("old content")
	if err := os.WriteFile(path, oldData, 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	newData := []byte("new content")
	err := Write(path, newData)
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	if !bytes.Equal(got, newData) {
		t.Errorf("content mismatch: got %q, want %q", got, newData)
	}
}

func TestWriteCreatesParentDirs(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "a", "b", "c", "file.txt")
	data := []byte("nested file")

	err := Write(path, data)
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	if !bytes.Equal(got, data) {
		t.Errorf("content mismatch: got %q, want %q", got, data)
	}
}

func TestWriteCleansUpOnError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: read-only dirs are still writable")
	}
	tmpDir := t.TempDir()
	readOnlyDir := filepath.Join(tmpDir, "readonly")

	if err := os.Mkdir(readOnlyDir, 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	// Make directory read-only
	if err := os.Chmod(readOnlyDir, 0o555); err != nil {
		t.Fatalf("chmod failed: %v", err)
	}

	// Restore permissions for cleanup
	defer os.Chmod(readOnlyDir, 0o755)

	path := filepath.Join(readOnlyDir, "file.txt")
	data := []byte("should fail")

	err := Write(path, data)
	if err == nil {
		t.Fatal("Write should have failed on read-only directory")
	}

	// Check no temp files left behind
	entries, err := os.ReadDir(readOnlyDir)
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}

	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".gale-tmp-") {
			t.Errorf("temp file not cleaned up: %s", entry.Name())
		}
	}
}

func TestWriteContentMatchesExactly(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "exact.bin")

	// Use specific bytes including nulls, special chars
	data := []byte{0x00, 0xFF, 0x42, 0x13, 0x37, 'h', 'e', 'l', 'l', 'o', 0x00}

	err := Write(path, data)
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	if !bytes.Equal(got, data) {
		t.Errorf("byte mismatch: got %v, want %v", got, data)
	}
}

// TestWritePreservesSymlinkAtTargetPath pins gh#193: the rename
// replaced a symlink at the target path with a regular file, so a
// dotfile-managed gale.toml or gale.lock was destroyed by the first
// write and the link's old target went stale. A write replaces the
// contents of the entry the caller named; it never changes that
// entry's type.
func TestWritePreservesSymlinkAtTargetPath(t *testing.T) {
	dir := t.TempDir()
	old := []byte("old")
	link, target := seedLink(
		t, dir, "gale.toml", filepath.Join("dotfiles", "gale.toml"), old,
	)

	if err := Write(link, []byte("new")); err == nil {
		t.Error("Write replaced a symlink, want a refusal")
	}

	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("target path is gone: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf(
			"target path is now a %s, want the symlink preserved",
			fi.Mode(),
		)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("reading the link target: %v", err)
	}
	if !bytes.Equal(got, old) {
		t.Errorf("link target content: got %q, want %q", got, old)
	}
}

// TestWriteRefusesUnresolvableSymlink covers the links a write cannot
// follow even in principle. Each one used to be reported as a
// successful write, because os.Stat failing left the mode probe at
// its default and the rename then clobbered the link.
func TestWriteRefusesUnresolvableSymlink(t *testing.T) {
	const link = "gale.toml"
	cases := []struct {
		name   string
		target string
		setup  func(t *testing.T, dir string)
	}{
		{name: "dangling", target: "missing.toml"},
		{name: "loop", target: link},
		{name: "directory target", target: "adir", setup: func(t *testing.T, dir string) {
			t.Helper()
			if err := os.Mkdir(filepath.Join(dir, "adir"), 0o755); err != nil {
				t.Fatalf("setup: %v", err)
			}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.setup != nil {
				tc.setup(t, dir)
			}
			linkPath, target := seedLink(t, dir, link, tc.target, nil)

			if err := Write(linkPath, []byte("new")); err == nil {
				t.Error("Write returned nil, want a refusal")
			}

			fi, err := os.Lstat(linkPath)
			if err != nil {
				t.Fatalf("target path is gone: %v", err)
			}
			if fi.Mode()&os.ModeSymlink == 0 {
				t.Errorf(
					"target path is now a %s, want the symlink preserved",
					fi.Mode(),
				)
			}

			// The link target must be left as the fixture made it:
			// absent for the dangling case, the link itself for the
			// loop, a directory for the third. A regular file there
			// means the write resolved through the link.
			tfi, err := os.Lstat(target)
			switch {
			case errors.Is(err, fs.ErrNotExist):
			case err != nil:
				t.Fatalf("lstat link target: %v", err)
			case tfi.Mode().IsRegular():
				t.Errorf("write landed a regular file at the link target %s", target)
			}

			// The refusal precedes os.CreateTemp, so it leaves no
			// residue either.
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatalf("ReadDir failed: %v", err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".gale-tmp-") {
					t.Errorf("temp file not cleaned up: %s", entry.Name())
				}
			}
		})
	}
}

func TestConcurrentWritesNoCorruption(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "concurrent.txt")

	const numWriters = 10
	var wg sync.WaitGroup
	wg.Add(numWriters)

	for i := 0; i < numWriters; i++ {
		go func(id int) {
			defer wg.Done()
			data := []byte(string(rune('0' + id)))
			if err := Write(path, data); err != nil {
				t.Errorf("goroutine %d: Write failed: %v", id, err)
			}
		}(i)
	}

	wg.Wait()

	// File should contain one of the values, not be corrupt
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("corrupt file: got %d bytes, want 1", len(got))
	}

	// Should be one of '0' through '9'
	char := got[0]
	if char < '0' || char > '9' {
		t.Errorf("corrupt content: got %q, want digit 0-9", char)
	}
}
