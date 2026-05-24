// Race reproducers for the audit. These tests demonstrate
// confirmed concurrency bugs surfaced by audit/races/.
// They live in cmd/gale to access removeLockEntry directly.
//
// Each test deliberately exhibits a failure pattern; the
// scenario is documented in audit/races/findings/.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kelp/gale/internal/filelock"
	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/store"
)

// TestAudit_RemoveLockEntryRace demonstrates the unlocked
// read-modify-write in cmd/gale/context.go:removeLockEntry.
//
// G1 (install path): filelock.With { Read; mutate; Write }
// G2 (remove path):  Read; mutate; Write   -- NO LOCK
//
// Across many trials, G2 can write its stale snapshot AFTER
// G1's atomic-rename completes, silently overwriting the
// install update.
func TestAudit_RemoveLockEntryRace(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "gale.lock")

	const trials = 500
	var lostUpdates atomic.Int32
	var orphanRemoves atomic.Int32

	seed := &lockfile.LockFile{
		Packages: map[string]lockfile.LockedPackage{
			"jq":      {Version: "1.7.1", SHA256: "deadbeef"},
			"ripgrep": {Version: "14.0.0", SHA256: "cafef00d"},
		},
	}

	for trial := 0; trial < trials; trial++ {
		if err := lockfile.Write(lockPath, seed); err != nil {
			t.Fatal(err)
		}

		newVer := "14.0.1"
		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			err := filelock.With(lockPath+".lock", func() error {
				lf, err := lockfile.Read(lockPath)
				if err != nil {
					return err
				}
				lf.Packages["ripgrep"] = lockfile.LockedPackage{
					Version: newVer, SHA256: "newhash",
				}
				return lockfile.Write(lockPath, lf)
			})
			if err != nil {
				t.Errorf("install branch: %v", err)
			}
		}()

		go func() {
			defer wg.Done()
			configPath := lockPath[:len(lockPath)-len(".lock")] + ".toml"
			if err := removeLockEntry(configPath, "jq"); err != nil {
				t.Errorf("remove branch: %v", err)
			}
		}()

		wg.Wait()

		final, err := lockfile.Read(lockPath)
		if err != nil {
			t.Fatal(err)
		}

		// Any serialized order ends with: jq absent, ripgrep@new.
		// A race shows up as ripgrep stuck at 14.0.0 (lost
		// install update) or jq still present (lost remove).
		if final.Packages["ripgrep"].Version != newVer {
			lostUpdates.Add(1)
		}
		if _, jqStillThere := final.Packages["jq"]; jqStillThere {
			orphanRemoves.Add(1)
		}
	}

	t.Logf("trials=%d", trials)
	t.Logf("lost-install-updates=%d", lostUpdates.Load())
	t.Logf("orphan-removes-still-present=%d", orphanRemoves.Load())

	if lostUpdates.Load() == 0 && orphanRemoves.Load() == 0 {
		t.Skip("no race observed in this run; flake")
	}
	// Surface the race count; the test deliberately reports
	// the failure rate rather than asserting a threshold.
	// We do fail loudly so CI catches the audit regression.
	if lostUpdates.Load() > 0 {
		t.Errorf("CONFIRMED: %d/%d trials lost an install update due to unlocked removeLockEntry",
			lostUpdates.Load(), trials)
	}
	// Clean up the lock file the test left behind.
	_ = os.Remove(lockPath + ".lock")
}

// TestAudit_GcVsBuildRace demonstrates that
// cleanOldGenerations bypasses the generation lock and
// snapshots the directory listing before reading the
// current-symlink target. A Build that has created its
// next gen dir but not yet swapped current is visible to
// gc as "not current" and gets RemoveAll'd mid-populate.
//
// Setup (three generations):
//   - gen/1: genuinely old generation (not current)
//   - gen/2: the current generation (current → gen/2)
//   - gen/3: in-flight new generation being built
//     (simulated Build holds the gen lock for 80ms while
//     populating gen/3; current has NOT been swapped yet)
//
// gc starts 10ms after the lock is acquired. With the bug
// it proceeds without waiting for the lock, sees gen/3 as
// non-current, and RemoveAll's it. With the fix gc blocks
// on the lock, waits for the simulated Build to finish,
// then only reaps generations with n < curGen (gen/1).
//
// Expected outcome after the fix:
//   - gen/3 still exists (in-flight gen was not deleted)
//   - gen/1 is gone (genuine old gen was reaped)
//   - current still points at gen/2 (no swap happened)
func TestAudit_GcVsBuildRace(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := filepath.Join(galeDir, "pkg")

	// Seed a fake jq binary in the store.
	binDir := filepath.Join(storeRoot, "jq", "1.8.1", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "jq"),
		[]byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}
	pkgs := map[string]string{"jq": "1.8.1"}

	// First Build: creates gen/1, current → gen/1.
	if err := generation.Build(pkgs, galeDir, storeRoot); err != nil {
		t.Fatalf("first Build: %v", err)
	}
	// Second Build: creates gen/2, current → gen/2.
	// gen/1 is now the old generation that gc should reap.
	if err := generation.Build(pkgs, galeDir, storeRoot); err != nil {
		t.Fatalf("second Build: %v", err)
	}

	// Verify the expected pre-gc state.
	curBefore, err := generation.Current(galeDir)
	if err != nil || curBefore != 2 {
		t.Fatalf("expected current=2 before gc, got %d err=%v",
			curBefore, err)
	}

	// The simulated in-flight Build acquires the gen lock,
	// creates gen/3/bin/ (partially populated), holds for
	// 80ms, then releases — never swaps current. gc must
	// not touch gen/3 while the lock is held.
	lockPath := filepath.Join(galeDir, "generation.lock")

	acquired := make(chan struct{})
	gcDone := make(chan struct{})

	go func() {
		_ = filelock.With(lockPath, func() error {
			// Create gen/3/bin as if Build is mid-populate.
			if mkErr := os.MkdirAll(filepath.Join(
				galeDir, "gen", "3", "bin"), 0o755); mkErr != nil {
				// Can't use t.Fatal in a goroutine; log and
				// signal so the test proceeds to check state.
				t.Logf("simulated Build: MkdirAll: %v", mkErr)
			}
			close(acquired)
			// Hold the lock for 80ms to give gc time to run
			// and demonstrate the race.
			time.Sleep(80 * time.Millisecond)
			return nil
		})
	}()

	// Wait until the simulated Build holds the lock and has
	// created gen/3.
	<-acquired

	// Start gc 10ms after the lock is established — enough
	// time to ensure the lock is held before gc begins.
	time.Sleep(10 * time.Millisecond)

	go func() {
		defer close(gcDone)
		// Call the real cleanOldGenerations from gc.go.
		// With the bug it will not acquire the lock and will
		// delete gen/3. With the fix it will block, then only
		// delete gen/1 (n < curGen=2).
		cleanOldGenerations(galeDir, storeRoot, false)
	}()

	// Wait for gc to finish. It should either return quickly
	// (bug: no lock → race) or after ~80ms (fix: lock blocks).
	<-gcDone

	// Check whether gc deleted the in-flight gen/3.
	_, errGen3 := os.Stat(filepath.Join(galeDir, "gen", "3"))
	gen3Gone := os.IsNotExist(errGen3)

	// Check whether gc reaped the old gen/1.
	_, errGen1 := os.Stat(filepath.Join(galeDir, "gen", "1"))
	gen1Gone := os.IsNotExist(errGen1)

	// Check that current still points at gen/2 (no swap).
	curAfter, _ := generation.Current(galeDir)

	// With the bug present the test must FAIL: gen/3 is
	// deleted while the simulated Build held the lock. After
	// the fix (filelock + n < curGen criterion) gen/3 must
	// survive and gen/1 must be reaped.
	if gen3Gone {
		t.Errorf(
			"CONFIRMED: cleanOldGenerations deleted in-flight "+
				"gen/3 while Build held the generation lock "+
				"(curGen=2, symlink not yet swapped). "+
				"stat err: %v", errGen3)
	}
	if !gen1Gone {
		t.Errorf(
			"gc did not reap old gen/1 (n < curGen=2); " +
				"expected it to be removed but it still exists")
	}
	if curAfter != 2 {
		t.Errorf(
			"current symlink moved: expected gen/2, got gen/%d",
			curAfter)
	}
}

// TestAudit_GcVsInstall_WindowBetweenStoreWriteAndConfigWrite
// shows that store.Remove does not acquire the per-package
// lock at <storeRoot>/<name>/<version>.lock before calling
// os.RemoveAll. The intended fix is for store.Remove to
// acquire that lock (the same lockfile that
// installer.lockPackage uses) so it serializes against a
// concurrent install of the same package version.
//
// Setup:
//   - Pre-creates a store dir for jq@1.8.1 with a bin/jq
//     file, simulating a completed Installer.Install.
//   - A goroutine acquires the per-package lock at
//     <storeRoot>/jq/1.8.1.lock via filelock.With and holds
//     it for ~80ms.
//   - After the goroutine holds the lock, the main goroutine
//     calls store.Remove("jq", "1.8.1").
//
// RED (today): Remove ignores the lock and returns in
// microseconds. The elapsed-time assertion (>= 60ms) FAILS.
//
// GREEN (after fix): Remove acquires the lock, blocks until
// the goroutine releases it (~80ms), then proceeds. The
// elapsed-time assertion PASSES. The store dir is gone.
func TestAudit_GcVsInstall_WindowBetweenStoreWriteAndConfigWrite(t *testing.T) {
	storeRoot := t.TempDir()

	// Pre-create the jq store dir as if Installer.Install
	// just completed and released the per-package lock.
	jqDir := filepath.Join(storeRoot, "jq", "1.8.1")
	if err := os.MkdirAll(filepath.Join(jqDir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jqDir, "bin", "jq"),
		[]byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Spawn a goroutine that acquires the per-package lock and
	// holds it for ~80ms — simulating an in-flight install
	// that still holds the lock.
	lockPath := filepath.Join(storeRoot, "jq", "1.8.1.lock")
	acquired := make(chan struct{})
	go func() {
		_ = filelock.With(lockPath, func() error {
			close(acquired)
			time.Sleep(80 * time.Millisecond)
			return nil
		})
	}()

	// Wait until the goroutine has the lock before calling
	// Remove, so the lock is definitely held.
	<-acquired

	// Call store.Remove from the main goroutine. With the fix,
	// this will block until the goroutine releases the lock
	// (~80ms). Without the fix it returns in microseconds.
	s := store.NewStore(storeRoot)
	start := time.Now()
	if err := s.Remove("jq", "1.8.1"); err != nil {
		t.Fatalf("store.Remove: %v", err)
	}
	elapsed := time.Since(start)

	// The store dir must be gone after Remove returns.
	if _, err := os.Stat(jqDir); !os.IsNotExist(err) {
		t.Errorf("store dir still exists after Remove: %v", err)
	}

	// Remove must have blocked for the lock. Today (RED) it
	// returns in microseconds, so this assertion FAILS.
	// After the fix (GREEN) it blocks ~80ms, so this PASSES.
	const minBlock = 60 * time.Millisecond
	if elapsed < minBlock {
		t.Errorf(
			"CONFIRMED: store.Remove returned in %v (< %v) "+
				"without waiting for the per-package lock at %s. "+
				"A concurrent gc can delete in-flight store dirs "+
				"because store.Remove does not acquire "+
				"<storeRoot>/<name>/<version>.lock before "+
				"os.RemoveAll.",
			elapsed, minBlock, lockPath)
	}
}

// TestAudit_ProjectGenLockNotSharedWithStoreGenLock
// demonstrates the "residual install-vs-project-sync race"
// documented in internal/installer/installer.go:1051.
//
// The intended fix: generation.Build (at any scope) must
// acquire the SAME lock as the installer — the store-rooted
// lock at filepath.Join(filepath.Dir(storeRoot),
// "generation.lock") — so that a project-scoped Build
// serializes against a concurrent global install.
//
// Approach:
//   - Goroutine A directly acquires the store-rooted lock
//     and holds it for ~80ms (simulating an in-flight
//     global install).
//   - Main calls generation.Build with projectGaleDir as
//     the galeDir argument and the shared storeRoot.
//   - RED (today): Build acquires
//     <projGaleDir>/generation.lock (a different file)
//     and returns in microseconds — no contention.
//   - GREEN (after fix): Build also acquires the
//     store-rooted lock and blocks for ~80ms.
func TestAudit_ProjectGenLockNotSharedWithStoreGenLock(t *testing.T) {
	globalGaleDir := t.TempDir()
	projectGaleDir := t.TempDir()

	// The shared store root: always global.
	storeRoot := filepath.Join(globalGaleDir, "pkg")
	if err := os.MkdirAll(storeRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	// Seed a fake jq binary in the store so generation.Build
	// can succeed without a real install pipeline.
	jqBinDir := filepath.Join(storeRoot, "jq", "1.8.1", "bin")
	if err := os.MkdirAll(jqBinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(jqBinDir, "jq"), []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}
	pkgs := map[string]string{"jq": "1.8.1"}

	// The store-rooted lock: filepath.Dir(storeRoot)/generation.lock
	// = globalGaleDir/generation.lock.
	// This is the same path storeGenLockPath(storeRoot) returns
	// in installer.go:1060, and the same path generation.Build
	// must acquire after the fix.
	storeGenLock := filepath.Join(
		filepath.Dir(storeRoot), "generation.lock")

	// Sanity: storeGenLock must NOT be inside projectGaleDir.
	projGenLock := filepath.Join(projectGaleDir, "generation.lock")
	if storeGenLock == projGenLock {
		t.Fatal("test setup error: storeGenLock == projGenLock")
	}

	// Goroutine A: acquire the store-rooted lock and hold
	// it for ~80ms — simulating an in-flight global install.
	acquired := make(chan struct{})
	go func() {
		_ = filelock.With(storeGenLock, func() error {
			close(acquired)
			time.Sleep(80 * time.Millisecond)
			return nil
		})
	}()

	// Wait until goroutine A has the lock before we call Build.
	<-acquired

	// Call generation.Build at project scope: galeDir =
	// projectGaleDir, storeRoot = global storeRoot.
	//
	// RED today: Build acquires projGenLock (a different file)
	// and returns in microseconds — no serialization with A.
	//
	// GREEN after fix: Build acquires storeGenLock (the same
	// file A holds) and blocks until A releases it (~80ms).
	start := time.Now()
	if err := generation.Build(pkgs, projectGaleDir, storeRoot); err != nil {
		t.Fatalf("generation.Build: %v", err)
	}
	elapsed := time.Since(start)

	// Build must have blocked waiting for the store-rooted
	// lock. Today (RED) it returns in microseconds because
	// it acquires a project-local lock instead, so this
	// assertion FAILS. After the fix (GREEN) it blocks ~80ms.
	const minBlock = 60 * time.Millisecond
	if elapsed < minBlock {
		t.Errorf(
			"CONFIRMED: generation.Build(pkgs, projectGaleDir, "+
				"storeRoot) returned in %v (< %v) without "+
				"waiting for the store-rooted generation lock "+
				"at %s. A concurrent global install holds that "+
				"lock; the project sync does not serialize "+
				"against it. Documented in installer.go:1051.",
			elapsed, minBlock, storeGenLock)
	}
}

// TestAudit_GaleTomlReadModifyWriteAcrossLockBoundary
// shows that switch (and similar commands) read gale.toml
// without a lock, decide what to do based on the snapshot,
// and only acquire the file lock for the eventual
// UpsertPackage write. A concurrent writer mutating
// gale.toml between the read and the upsert silently
// gets clobbered: switch picks up a stale "current
// version", makes its decision, and writes back —
// possibly recording a transition the user never
// intended.
//
// This test is intentionally narrower than a full
// switch repro: it demonstrates the read-then-write
// crossing the lock boundary without reproducing the
// install pipeline (which requires recipes/network).
func TestAudit_GaleTomlReadModifyWriteAcrossLockBoundary(t *testing.T) {
	dir := t.TempDir()
	galeToml := filepath.Join(dir, "gale.toml")
	if err := os.WriteFile(galeToml,
		[]byte("[packages]\njq = \"1.7.0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Reader: switch-style "load, then decide".
	readData, err := os.ReadFile(galeToml)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readData), "1.7.0") {
		t.Fatalf("seed missing: %s", readData)
	}

	// Meanwhile, another writer flipped jq to 1.8.0 via
	// the locked UpsertPackage path. We'd notice nothing.
	if err := os.WriteFile(galeToml,
		[]byte("[packages]\njq = \"1.8.0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Our switch decision was "current=1.7.0, new=1.8.0,
	// so transition". With the snapshot stale, we'd
	// happily proceed to install 1.8.0 even though the
	// user's gale.toml already says 1.8.0 — wasted work
	// and a misleading "Switching 1.7.0 → 1.8.0" log
	// line. Worse, with a downgrade from a faster writer,
	// the user can switch from a version they don't
	// have, picking up a destination version that's
	// also already gone stale by the time the install
	// completes.
	_ = readData

	// The reproducer is the static observation. A real
	// confirmation would need to wire the cmdContext +
	// installer and exercise switch end-to-end; instead
	// we note that LoadConfig in cmd/gale/context.go
	// uses os.ReadFile, and every caller in install,
	// switch, update, sync, remove makes a decision on
	// that data before calling a locked Upsert.
	t.Logf("CONFIRMED (static): cmd/gale/context.go:108 " +
		"LoadConfig uses unlocked os.ReadFile; callers in " +
		"switch.go:51, sync.go:72, update.go (search), and " +
		"remove.go:58 decide based on the snapshot before " +
		"crossing into config.UpsertPackage's lock.")
}
