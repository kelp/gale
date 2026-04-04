package atomicfile

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

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
		if filepath.HasPrefix(entry.Name(), ".gale-tmp-") {
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
