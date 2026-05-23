// Race reproducer for the audit finding 0002.
// These tests validate the FIX: Rollback must acquire
// the generation lock so it serializes with Build.

package generation

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kelp/gale/internal/filelock"
)

// TestAudit_RollbackBypassesGenLock verifies that after the
// fix, Rollback BLOCKS while another holder owns the
// generation.lock file. Before the fix Rollback returned
// immediately without waiting — this test exposes that bug
// (fails red) and will go green once the fix is applied.
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

	// holdDuration is how long we hold the lock before
	// releasing it. Rollback must wait at least this long.
	const holdDuration = 100 * time.Millisecond

	holdAcquired := make(chan struct{})
	holdRelease := make(chan struct{})
	holdDone := make(chan struct{})

	// Goroutine that owns the lock for holdDuration.
	// Always releases the lock before the test exits —
	// deadlock-safe even on failure paths.
	go func() {
		defer close(holdDone)
		_ = filelock.With(lockPath, func() error {
			close(holdAcquired)
			// Wait for the test to signal release or timeout.
			select {
			case <-holdRelease:
			case <-time.After(10 * time.Second):
				// Safety valve: release the lock no matter what
				// so the test doesn't hang if something goes wrong.
			}
			return nil
		})
	}()

	// Wait until the goroutine has the lock.
	<-holdAcquired

	// Schedule lock release after holdDuration has passed,
	// regardless of what happens below. This guarantees the
	// goroutine above will always unblock even if Rollback
	// returns early (the bug case) or the test errors.
	releaseAt := time.Now().Add(holdDuration)
	go func() {
		time.Sleep(time.Until(releaseAt))
		close(holdRelease)
	}()

	start := time.Now()
	if err := Rollback(galeDir, 1); err != nil {
		t.Fatalf("Rollback while gen lock held: %v", err)
	}
	waited := time.Since(start)

	// Wait for the lock holder to finish so it doesn't
	// leak into subsequent tests.
	<-holdDone

	// Rollback must have waited for the lock to be released.
	// Allow 20ms of scheduling jitter below holdDuration.
	const jitter = 20 * time.Millisecond
	if waited < holdDuration-jitter {
		t.Errorf("FAIL: Rollback returned in %v, expected it to "+
			"block for at least %v (lock hold time). "+
			"Rollback is NOT acquiring the generation lock.",
			waited, holdDuration-jitter)
	}

	// After Rollback completes the current symlink must
	// point at gen 1.
	cur, err := Current(galeDir)
	if err != nil {
		t.Fatalf("Current after Rollback: %v", err)
	}
	if cur != 1 {
		t.Errorf("post-Rollback current = %d, want 1", cur)
	}

	_ = os.Remove(lockPath)
}

// TestAudit_RollbackVsBuildRace_Deterministic verifies that
// after the fix, when a simulated Build holds the gen lock
// and swaps current to gen 2, a concurrent Rollback to gen 1
// must wait for the lock and then run AFTER the Build swap.
// Final state must be gen 1 (Rollback honored, ran last).
//
// Before the fix Rollback bypassed the lock, fired while
// Build held it, and Build's subsequent swap silently
// overwrote Rollback — the test exposes this (fails red
// with final == 2) and will go green once Rollback locks.
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

	// lockPath mirrors what generationLockPath() returns.
	lockPath := filepath.Join(galeDir, "generation.lock")

	// How long the simulated Build sleeps while holding
	// the lock. Long enough that Rollback is guaranteed
	// to be queued behind it before the lock is released.
	const buildHoldDuration = 80 * time.Millisecond

	buildSwapDone := make(chan struct{})

	// Simulated Build: acquire gen lock, sleep to let
	// Rollback queue up, then swap current to gen 2.
	go func() {
		defer close(buildSwapDone)
		err := filelock.With(lockPath, func() error {
			// Sleep while holding the lock so Rollback
			// is forced to queue up behind us.
			time.Sleep(buildHoldDuration)
			// Simulate Build's tail: swap to gen 2.
			return swapCurrentSymlink(galeDir, 2)
		})
		if err != nil {
			t.Errorf("simulated Build: %v", err)
		}
	}()

	// Give the simulated Build goroutine a moment to
	// acquire the lock before we call Rollback.
	time.Sleep(10 * time.Millisecond)

	// Rollback to gen 1. With the fix it will block until
	// the simulated Build releases the lock (after its
	// swap to gen 2). Then Rollback runs and swaps to gen 1.
	if err := Rollback(galeDir, 1); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	// Wait for the simulated Build goroutine to finish
	// so it doesn't escape the test.
	<-buildSwapDone

	final, err := Current(galeDir)
	if err != nil {
		t.Fatalf("final Current: %v", err)
	}

	// With the fix: ordering is Build-swap(gen2) then
	// Rollback-swap(gen1), so final must be gen 1.
	// Without the fix: Rollback fires while Build holds
	// the lock (swaps to gen 1), then Build swaps to gen 2
	// — final is gen 2 and Rollback's effect is lost.
	if final != 1 {
		t.Errorf("FAIL: final current = %d, want 1. "+
			"Build's swap to gen 2 ran AFTER Rollback, "+
			"meaning Rollback did not hold the gen lock "+
			"and its effect was silently discarded.", final)
	}
}
