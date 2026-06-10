package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// TestWriteCacheEntryReplacesDirectory pins F42: writeCacheEntry must
// replace the entry directory atomically (dir rename), not update
// individual files in-place. When the directory is replaced, its
// inode number changes; when only the contents are updated in-place,
// the inode stays constant.
//
// This test is deterministic and fails against the pre-fix two-
// separate-renames implementation (where body and etag were written
// into the same existing entryDir, leaving its inode unchanged).
func TestWriteCacheEntryReplacesDirectory(t *testing.T) {
	cacheDir := t.TempDir()
	entryDir := filepath.Join(cacheDir, "registry", "entry")

	// First write establishes the entry.
	writeCacheEntry(entryDir, []byte("body-v1"), `"etag-v1"`)

	info1, err := os.Stat(entryDir)
	if err != nil {
		t.Fatalf("stat after first write: %v", err)
	}
	inode1 := info1.Sys().(*syscall.Stat_t).Ino

	// Second write with different content. If writeCacheEntry replaces
	// the directory (RemoveAll + Rename), the inode changes. If it only
	// renames individual files in-place, the inode stays the same.
	writeCacheEntry(entryDir, []byte("body-v2"), `"etag-v2"`)

	info2, err := os.Stat(entryDir)
	if err != nil {
		t.Fatalf("stat after second write: %v", err)
	}
	inode2 := info2.Sys().(*syscall.Stat_t).Ino

	if inode1 == inode2 {
		t.Errorf("entryDir inode unchanged (%d) — writeCacheEntry updated "+
			"files in-place instead of replacing the directory atomically; "+
			"concurrent writers can produce a mismatched body/etag pair",
			inode1)
	}
}

// TestWriteCacheEntryPreservesOldEntryOnPromoteFailure pins the
// Bugbot follow-up to F42: when promoting the staging directory
// over entryDir fails, the previously valid body/etag pair must
// survive. The pre-fix implementation RemoveAll'd entryDir before
// the rename, so a failed rename discarded both the staged update
// (deferred cleanup) and the old entry — breaking offline and
// stale-on-error reads until a refetch succeeded.
func TestWriteCacheEntryPreservesOldEntryOnPromoteFailure(t *testing.T) {
	cacheDir := t.TempDir()
	entryDir := filepath.Join(cacheDir, "registry", "entry")

	// First write establishes the entry.
	writeCacheEntry(entryDir, []byte("body-v1"), `"etag-v1"`)

	// Fail only the promote rename (staging dir → entryDir); the
	// backup and restore renames must keep working.
	origRename := renameDir
	renameDir = func(oldPath, newPath string) error {
		if newPath == entryDir &&
			strings.HasPrefix(filepath.Base(oldPath), ".gale-cache-tmp-") {
			return fmt.Errorf("boom")
		}
		return origRename(oldPath, newPath)
	}
	defer func() { renameDir = origRename }()

	writeCacheEntry(entryDir, []byte("body-v2"), `"etag-v2"`)

	body, err := os.ReadFile(filepath.Join(entryDir, "body"))
	if err != nil {
		t.Fatalf("old entry lost after failed promote: %v", err)
	}
	if string(body) != "body-v1" {
		t.Errorf("body = %q, want %q", body, "body-v1")
	}
	etag, err := os.ReadFile(filepath.Join(entryDir, "etag"))
	if err != nil {
		t.Fatalf("old etag lost after failed promote: %v", err)
	}
	if string(etag) != `"etag-v1"` {
		t.Errorf("etag = %q, want %q", etag, `"etag-v1"`)
	}
}

// TestWriteCacheEntryCleansStaleBackupLeftover pins the crash-
// recovery half of the backup-rename dance: a <entryDir>.old
// directory left behind by a crash mid-write must not block the
// next write, and must be cleaned up by it.
func TestWriteCacheEntryCleansStaleBackupLeftover(t *testing.T) {
	cacheDir := t.TempDir()
	entryDir := filepath.Join(cacheDir, "registry", "entry")

	writeCacheEntry(entryDir, []byte("body-v1"), `"etag-v1"`)

	// Simulate a crash leftover: a populated .old backup dir.
	staleOld := entryDir + ".old"
	if err := os.MkdirAll(staleOld, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staleOld, "body"), []byte("crash"), 0o644); err != nil {
		t.Fatal(err)
	}

	writeCacheEntry(entryDir, []byte("body-v2"), `"etag-v2"`)

	body, err := os.ReadFile(filepath.Join(entryDir, "body"))
	if err != nil {
		t.Fatalf("read body after write over stale .old: %v", err)
	}
	if string(body) != "body-v2" {
		t.Errorf("body = %q, want %q", body, "body-v2")
	}
	if _, err := os.Stat(staleOld); !os.IsNotExist(err) {
		t.Errorf("stale %s still present after successful write", staleOld)
	}
}
