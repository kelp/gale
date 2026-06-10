package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kelp/gale/internal/filelock"
)

// TestRemoveDoesNotDeleteHeldLockFile is the regression test
// for gh#77: cleanupEmptyNameDir (called from Store.Remove)
// unlinked every *.lock file in <store>/<name>/ when no
// version dirs remained — including a lock file a concurrent
// installer held via flock. Once the held file is unlinked, a
// later filelock.Acquire of the same path creates a fresh
// inode and succeeds immediately, so two installers of the
// same name@version run concurrently.
//
// Scenario (single process; flock conflicts apply across fds):
//   - Process C: holds foo/2.0-1.lock (the window between
//     lockPackage and Store.Create during a long dep build).
//   - Process B: removes the only installed version foo/1.0-1.
//
// RED (pre-fix): Remove unlinks the held foo/2.0-1.lock and a
// second Acquire succeeds instantly. GREEN: lock files are
// never deleted; the second Acquire blocks until C releases.
func TestRemoveDoesNotDeleteHeldLockFile(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)

	// The only installed version, about to be removed.
	dir, err := s.Create("foo", "1.0-1")
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(dir, "marker"), []byte("x"), 0o644,
	); err != nil {
		t.Fatalf("seed store file: %v", err)
	}

	// A concurrent installer holds the per-package lock for a
	// DIFFERENT version of the same package.
	heldLockPath := filepath.Join(root, "foo", "2.0-1.lock")
	unlock, err := filelock.Acquire(heldLockPath)
	if err != nil {
		t.Fatalf("acquire held lock: %v", err)
	}
	defer unlock()

	if err := s.Remove("foo", "1.0-1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// The held lock file must survive Remove.
	if _, err := os.Stat(heldLockPath); err != nil {
		t.Fatalf("held lock file was deleted by Remove — "+
			"flock mutual exclusion broken (gh#77): %v", err)
	}

	// And a second contender on the same path must still block
	// behind the original holder — proving both open the same
	// inode.
	acquired := make(chan struct{})
	go func() {
		unlock2, err := filelock.Acquire(heldLockPath)
		if err == nil {
			close(acquired)
			unlock2()
		}
	}()
	select {
	case <-acquired:
		t.Fatal("second Acquire succeeded while first lock " +
			"still held — mutual exclusion broken (gh#77)")
	case <-time.After(150 * time.Millisecond):
		// Good: the second contender is blocked.
	}
	unlock()
	select {
	case <-acquired:
	case <-time.After(10 * time.Second):
		t.Fatal("second Acquire never completed after release")
	}
}
