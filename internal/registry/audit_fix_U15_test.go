package registry

import (
	"os"
	"path/filepath"
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
