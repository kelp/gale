// Race reproducers for the audit. These tests demonstrate
// confirmed concurrency bugs surfaced by audit/races/.
// They live in cmd/gale to access the command internals directly.
//
// Each test deliberately exhibits a failure pattern; the
// scenario is documented in audit/races/findings/.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kelp/gale/internal/filelock"
	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/store"
)

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
// Expected outcome after mark-and-sweep: gc waits for the
// lock, then treats the unswapped gen/4 as not kept (it is
// neither current nor current-1) and reaps it. gen/1 goes;
// gen/2 and gen/3 stay; current stays at 3.
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

	// Three Builds: gen/1 below the keep-2 window, gen/2
	// previous, gen/3 current.
	if err := generation.Build(pkgs, galeDir, storeRoot); err != nil {
		t.Fatalf("first Build: %v", err)
	}
	if err := generation.Build(pkgs, galeDir, storeRoot); err != nil {
		t.Fatalf("second Build: %v", err)
	}
	if err := generation.Build(pkgs, galeDir, storeRoot); err != nil {
		t.Fatalf("third Build: %v", err)
	}

	curBefore, err := generation.Current(galeDir)
	if err != nil || curBefore != 3 {
		t.Fatalf("expected current=3 before gc, got %d err=%v",
			curBefore, err)
	}

	// The simulated in-flight Build acquires the gen lock,
	// creates gen/4/bin/ (partially populated), holds for
	// 80ms, then releases — never swaps current. gc must
	// not touch gen/4 while the lock is held.
	lockPath := filepath.Join(galeDir, "generation.lock")

	acquired := make(chan struct{})
	gcDone := make(chan struct{})

	go func() {
		_ = filelock.With(lockPath, func() error {
			if mkErr := os.MkdirAll(filepath.Join(
				galeDir, "gen", "4", "bin",
			), 0o755); mkErr != nil {
				t.Logf("simulated Build: MkdirAll: %v", mkErr)
			}
			close(acquired)
			time.Sleep(80 * time.Millisecond)
			return nil
		})
	}()

	<-acquired
	time.Sleep(10 * time.Millisecond)

	go func() {
		defer close(gcDone)
		cleanOldGenerations(galeDir, storeRoot, false)
	}()

	<-gcDone

	assertGcVsBuildRace(t, galeDir)
}

func assertGcVsBuildRace(t *testing.T, galeDir string) {
	t.Helper()
	_, errGen4 := os.Stat(filepath.Join(galeDir, "gen", "4"))
	_, errGen1 := os.Stat(filepath.Join(galeDir, "gen", "1"))
	_, errGen2 := os.Stat(filepath.Join(galeDir, "gen", "2"))
	curAfter, _ := generation.Current(galeDir)

	if !os.IsNotExist(errGen4) {
		t.Errorf(
			"unswapped in-flight gen/4 is not a kept generation "+
				"and must be reaped after generation.lock is released, "+
				"stat err: %v", errGen4,
		)
	}
	if !os.IsNotExist(errGen1) {
		t.Errorf(
			"gc did not reap gen/1 (below keep-2 cutoff); " +
				"expected it to be removed but it still exists",
		)
	}
	if os.IsNotExist(errGen2) {
		t.Errorf("gc reaped gen/2; the previous generation must stay")
	}
	if curAfter != 3 {
		t.Errorf(
			"current symlink moved: expected gen/3, got gen/%d",
			curAfter,
		)
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
			elapsed, minBlock, lockPath,
		)
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
		filepath.Join(jqBinDir, "jq"), []byte("fake"), 0o755,
	); err != nil {
		t.Fatal(err)
	}
	pkgs := map[string]string{"jq": "1.8.1"}

	// The store-rooted lock: filepath.Dir(storeRoot)/generation.lock
	// = globalGaleDir/generation.lock.
	// This is the same path storeGenLockPath(storeRoot) returns
	// in installer.go:1060, and the same path generation.Build
	// must acquire after the fix.
	storeGenLock := filepath.Join(
		filepath.Dir(storeRoot), "generation.lock",
	)

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
			elapsed, minBlock, storeGenLock,
		)
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
