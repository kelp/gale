package generation

// Red-green tests for the U2 rollback-lock audit unit:
//
//   gh#45 — Rollback validates the target gen's existence
//   outside the generation lock; a concurrent prune between
//   check and swap leaves current dangling.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kelp/gale/internal/filelock"
)

// gh#45: the target gen's existence check must happen under
// the generation lock. A concurrent prune (autoPruneGenerations
// after a Build) can delete the target while Rollback waits on
// the lock; checking outside lets the swap land a dangling
// current symlink while reporting success.
func TestRollbackChecksTargetGenUnderLock(t *testing.T) {
	// The store lives INSIDE the gale dir, as it does in
	// production: filepath.Dir(storeRoot) is the global gale dir,
	// an invariant storeGenLockPath rests on. Two unrelated temp
	// dirs would break it.
	galeDir := t.TempDir()
	storeRoot := filepath.Join(galeDir, "pkg")
	createStoreEntry(t, storeRoot, "jq", "1.0", []string{"jq"})
	pkgs := map[string]string{"jq": "1.0"}
	if err := Build(pkgs, galeDir, storeRoot); err != nil {
		t.Fatalf("Build gen 1: %v", err)
	}
	if err := Build(pkgs, galeDir, storeRoot); err != nil {
		t.Fatalf("Build gen 2: %v", err)
	}

	// Hold the generation lock, exactly as a concurrent
	// Build + auto-prune would.
	lockPath := filepath.Join(filepath.Dir(storeRoot), "generation.lock")
	unlock, err := filelock.Acquire(lockPath)
	if err != nil {
		t.Fatalf("acquire generation lock: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- Rollback(galeDir, storeRoot, 1) }()

	// Let Rollback pass any pre-lock existence check and
	// block on the lock, then prune the target gen — the
	// interleaving auto-prune produces.
	time.Sleep(200 * time.Millisecond)
	if err := os.RemoveAll(
		filepath.Join(galeDir, "gen", "1"),
	); err != nil {
		t.Fatalf("prune target gen: %v", err)
	}
	unlock()

	if err := <-done; err == nil {
		target, _ := os.Readlink(filepath.Join(galeDir, "current"))
		t.Fatalf(
			"Rollback succeeded after the target gen was pruned; "+
				"current -> %s now dangles", target,
		)
	}

	// The failed rollback must leave current resolving.
	if _, err := os.Stat(filepath.Join(galeDir, "current")); err != nil {
		t.Errorf("current dangles after failed rollback: %v", err)
	}
}
