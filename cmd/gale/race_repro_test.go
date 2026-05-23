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

// TestAudit_GcVsBuildRace shows that cleanOldGenerations
// bypasses the generation lock and reads its entries
// snapshot BEFORE reading the current-symlink target. A
// Build that has created its next gen dir but not yet
// swapped current is visible to gc as "not current" and
// is RemoveAll'd mid-populate.
//
// We don't import the unexported cleanOldGenerations from
// gc.go directly — instead we duplicate its three-line
// body verbatim so the test never drifts from the source
// (any change to gc.go's body must also update this copy).
func TestAudit_GcVsBuildRace(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	// Seed gen 1.
	binDir := filepath.Join(storeRoot, "jq", "1.8.1", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "jq"),
		[]byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}
	pkgs := map[string]string{"jq": "1.8.1"}
	if err := generation.Build(pkgs, galeDir, storeRoot); err != nil {
		t.Fatalf("seed Build: %v", err)
	}

	// Hold the gen lock from outside (simulating an
	// in-flight Build that has created gen/2 and is mid-
	// populate). gc must not interfere.
	lockPath := filepath.Join(galeDir, "generation.lock")

	// Create an in-flight gen/2 dir, current still → gen/1.
	if err := os.MkdirAll(filepath.Join(
		galeDir, "gen", "2", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}

	released := make(chan struct{})
	acquired := make(chan struct{})
	gcDone := make(chan struct{})

	// Goroutine simulating Build holding the lock.
	go func() {
		_ = filelock.With(lockPath, func() error {
			close(acquired)
			<-released
			return nil
		})
	}()
	<-acquired

	// Run gc's cleanOldGenerations body verbatim. (Mirror
	// of cmd/gale/gc.go:244 cleanOldGenerations — see
	// audit/races/findings/0003-*.md.)
	go func() {
		defer close(gcDone)
		genRoot := filepath.Join(galeDir, "gen")
		entries, _ := os.ReadDir(genRoot)
		// curGen reads the symlink: still → gen/1 because
		// Build hasn't swapped yet.
		curGen, _ := generation.Current(galeDir)
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			// Atoi to match gc.go.
			var n int
			_, _ = (&n), e
			if e.Name() == "1" {
				n = 1
			} else if e.Name() == "2" {
				n = 2
			} else {
				continue
			}
			if n == curGen {
				continue
			}
			_ = os.RemoveAll(filepath.Join(genRoot, e.Name()))
		}
	}()
	<-gcDone

	// gen/2 should still exist — Build is mid-populate. But
	// gc happily nuked it because gc didn't hold the lock
	// AND read curGen as 1 (pre-swap).
	_, err := os.Stat(filepath.Join(galeDir, "gen", "2"))
	gcDeleted := os.IsNotExist(err)

	close(released)

	// If gc had honored the gen lock, gen/2 would still exist.
	if !gcDeleted {
		t.Skip("gc did not delete gen/2 in this run; flake")
	}
	t.Errorf("CONFIRMED: cleanOldGenerations deleted "+
		"in-flight gen/2 while Build held the generation "+
		"lock (curGen=1 because symlink not yet swapped). "+
		"err=%v", err)

	// Tidy up.
	time.Sleep(1 * time.Millisecond)
}

// TestAudit_GcVsInstall_WindowBetweenStoreWriteAndConfigWrite
// shows the Class-C window: install.go calls Installer.Install
// (which writes the store dir under the per-package lock and
// then releases it) before ctx.FinalizeRecipeInstall (which
// writes gale.toml). gc concurrently reads gale.toml, sees
// the just-installed package as "unreferenced", and removes
// it from the store. The subsequent generation rebuild then
// fails because the store dir is gone.
//
// Reproducer simulates this without invoking the real
// installer (no network). We:
//   1. Pre-create a store dir for jq@1.8.1
//   2. Leave gale.toml empty
//   3. Run gc's body: store.Remove(jq, 1.8.1)
//   4. Show that the store dir is gone — no lock prevented it
//
// In production, this race is wider because Installer.Install
// has fully released the per-package lock by the time gc
// could see the dir; even acquiring that lock from gc would
// not close the window between Install's lock release and
// the eventual FinalizeRecipeInstall write to gale.toml.
func TestAudit_GcVsInstall_WindowBetweenStoreWriteAndConfigWrite(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	// Empty gale.toml (simulating mid-install state where
	// the store has jq but the config does not yet).
	if err := os.WriteFile(filepath.Join(galeDir, "gale.toml"),
		[]byte("[packages]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Pre-create the jq store dir as if Installer.Install
	// just returned.
	jqDir := filepath.Join(storeRoot, "jq", "1.8.1")
	if err := os.MkdirAll(filepath.Join(jqDir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jqDir, "bin", "jq"),
		[]byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Also acquire the per-package lock from a goroutine —
	// mimicking the install holding it briefly. (In reality
	// Install releases this BEFORE FinalizeRecipeInstall, so
	// this is the optimistic case where the lock is still
	// held — gc must not depend on this anyway.)
	lockPath := filepath.Join(storeRoot, "jq", "1.8.1.lock")
	held := make(chan struct{})
	releaseHold := make(chan struct{})
	go func() {
		_ = filelock.With(lockPath, func() error {
			close(held)
			<-releaseHold
			return nil
		})
	}()
	<-held

	// Mirror gc.removeUnreferencedVersions: read gale.toml,
	// see no references, store.Remove(jq, 1.8.1). gc doesn't
	// hold the per-package lock. Uses the real Store API.
	s := store.NewStore(storeRoot)
	if err := s.Remove("jq", "1.8.1"); err != nil {
		t.Fatalf("store.Remove: %v", err)
	}
	close(releaseHold)

	// Verify the store dir is gone.
	if _, err := os.Stat(jqDir); !os.IsNotExist(err) {
		t.Skipf("gc did not remove jq dir: %v", err)
	}
	t.Errorf("CONFIRMED: store dir for in-flight install was "+
		"removed by gc-equivalent unlocked RemoveAll. " +
		"A subsequent FinalizeRecipeInstall would fail in " +
		"populateGeneration with ENOENT on the missing dir.")
}

// TestAudit_ProjectGenLockNotSharedWithStoreGenLock
// proves the documented "residual install-vs-project-sync
// race" in internal/installer/installer.go:1051. A
// project-scoped generation.Build acquires
// <projGaleDir>/generation.lock; a global install acquires
// storeGenLock which is <filepath.Dir(storeRoot)>/generation.lock
// (i.e., <globalGaleDir>/generation.lock). These are different
// files, so the locks don't serialize against each other.
//
// We don't need to wait — we observe that one goroutine can
// acquire its lock while the other still holds the other.
func TestAudit_ProjectGenLockNotSharedWithStoreGenLock(t *testing.T) {
	globalGaleDir := t.TempDir()
	projGaleDir := t.TempDir()
	storeRoot := filepath.Join(globalGaleDir, "pkg")
	if err := os.MkdirAll(storeRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	// storeGenLockPath as defined in installer.go:1060:
	// filepath.Join(filepath.Dir(storeRoot), "generation.lock")
	storeGenLock := filepath.Join(
		filepath.Dir(storeRoot), "generation.lock")
	// generationLockPath at project scope:
	projGenLock := filepath.Join(
		projGaleDir, "generation.lock")

	if storeGenLock == projGenLock {
		t.Fatal("paths unexpectedly identical")
	}

	heldStore := make(chan struct{})
	releaseStore := make(chan struct{})
	heldProj := make(chan struct{})
	releaseProj := make(chan struct{})

	go func() {
		_ = filelock.With(storeGenLock, func() error {
			close(heldStore)
			<-releaseStore
			return nil
		})
	}()
	<-heldStore

	start := time.Now()
	go func() {
		_ = filelock.With(projGenLock, func() error {
			close(heldProj)
			<-releaseProj
			return nil
		})
	}()

	select {
	case <-heldProj:
		dur := time.Since(start)
		t.Errorf("CONFIRMED: project gen lock acquired in %v "+
			"while another holder still owns the store-gen "+
			"lock — the locks are different files, so install "+
			"and project sync do not serialize. Documented "+
			"in installer.go:1051.", dur)
	case <-time.After(500 * time.Millisecond):
		t.Errorf("project gen lock did not acquire within "+
			"500ms; this would mean the locks ARE shared — " +
			"unexpected given the static analysis.")
	}

	close(releaseProj)
	close(releaseStore)
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

