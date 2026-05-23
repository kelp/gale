// Race reproducer for the audit. Demonstrates that
// Rollback bypasses the generation lock, so a concurrent
// Build (or any holder of the gen lock that ends with a
// swapCurrentSymlink) silently overrides Rollback.

package generation

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/kelp/gale/internal/filelock"
)

// TestAudit_RollbackVsBuildRace_Deterministic shows that
// Rollback bypasses the generation lock that Build holds
// for the duration of its populate-then-swap. With the
// lock missing, Build's swap is the last write and
// Rollback's effect is silently discarded.
func TestAudit_RollbackVsBuildRace_Deterministic(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	createStoreEntry(t, storeRoot, "jq", "1.8.1", []string{"jq"})

	pkgs := map[string]string{"jq": "1.8.1"}
	if err := Build(pkgs, galeDir, storeRoot); err != nil {
		t.Fatalf("seed Build 1: %v", err)
	}
	if err := Build(pkgs, galeDir, storeRoot); err != nil {
		t.Fatalf("seed Build 2: %v", err)
	}

	cur, err := Current(galeDir)
	if err != nil || cur != 2 {
		t.Fatalf("seed current: got %d err %v", cur, err)
	}

	// Simulate a long-running Build: acquire the gen lock,
	// hold it for ~100ms, then perform Build's tail swap
	// to gen 3. This mirrors what build() does, minus the
	// populate (already populated as gen 2 above; we just
	// reuse it as the swap target for simplicity — the
	// point is the SWAP, not the populate).
	//
	// We swap into gen 2 (which exists). During the hold,
	// Rollback should be free to swap current to gen 1
	// — if the lock semantics were honored, Rollback would
	// block until we released. With Rollback unlocked,
	// it fires immediately.
	lockPath := filepath.Join(galeDir, "generation.lock")

	var wg sync.WaitGroup
	wg.Add(2)

	rollbackReturned := make(chan struct{})

	// Simulated Build.
	go func() {
		defer wg.Done()
		err := filelock.With(lockPath, func() error {
			// Mimic populate work + the close-to-end swap.
			// Wait until Rollback has clearly returned, so
			// we can prove Build's swap overwrites it.
			select {
			case <-rollbackReturned:
			case <-time.After(2 * time.Second):
				t.Errorf("timed out waiting for Rollback")
				return nil
			}
			// Now do Build's tail swap to gen 2.
			return swapCurrentSymlink(galeDir, 2)
		})
		if err != nil {
			t.Errorf("simulated Build: %v", err)
		}
	}()

	// Rollback — should fire while Build holds the lock.
	go func() {
		defer wg.Done()
		// Tiny delay so Build acquires lock first.
		time.Sleep(10 * time.Millisecond)
		if err := Rollback(galeDir, 1); err != nil {
			t.Errorf("Rollback: %v", err)
		}
		// Immediately after Rollback returns, current should
		// be gen 1.
		c, _ := Current(galeDir)
		if c != 1 {
			t.Errorf("post-Rollback current: got %d, want 1", c)
		}
		close(rollbackReturned)
	}()

	wg.Wait()

	final, err := Current(galeDir)
	if err != nil {
		t.Fatalf("final current: %v", err)
	}

	if final != 1 {
		t.Errorf("CONFIRMED: Rollback to gen 1 was lost; final=%d "+
			"(Build's swap completed AFTER Rollback because "+
			"Rollback did not hold the gen lock)", final)
	}
}

// TestAudit_RollbackBypassesGenLock proves the lock claim
// directly: Rollback returns successfully even while
// another holder owns the generation.lock file. If
// Rollback honored the lock, it would block.
func TestAudit_RollbackBypassesGenLock(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	createStoreEntry(t, storeRoot, "jq", "1.8.1", []string{"jq"})
	pkgs := map[string]string{"jq": "1.8.1"}
	if err := Build(pkgs, galeDir, storeRoot); err != nil {
		t.Fatalf("seed Build 1: %v", err)
	}
	if err := Build(pkgs, galeDir, storeRoot); err != nil {
		t.Fatalf("seed Build 2: %v", err)
	}

	lockPath := filepath.Join(galeDir, "generation.lock")

	holdReleased := make(chan struct{})
	holdAcquired := make(chan struct{})

	go func() {
		_ = filelock.With(lockPath, func() error {
			close(holdAcquired)
			<-holdReleased
			return nil
		})
	}()
	<-holdAcquired

	start := time.Now()
	if err := Rollback(galeDir, 1); err != nil {
		t.Fatalf("Rollback while gen lock held: %v", err)
	}
	dur := time.Since(start)

	close(holdReleased)

	if dur > 100*time.Millisecond {
		t.Logf("Rollback waited for lock (took %v) — not racy",
			dur)
		return
	}
	c, _ := Current(galeDir)
	if c != 1 {
		t.Errorf("Rollback completed without waiting "+
			"but current=%d (expected 1)", c)
	}
	t.Errorf("CONFIRMED: Rollback completed in %v despite "+
		"another holder owning generation.lock — Rollback "+
		"bypasses the lock", dur)

	_ = os.Remove(lockPath)
}
