package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/kelp/gale/internal/depsmeta"
	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/lockplan"
	"github.com/kelp/gale/internal/provenance"
	"github.com/kelp/gale/internal/store"
)

func TestSyncBuildFlagReplacesSource(t *testing.T) {
	// --build must exist.
	f := syncCmd.Flags().Lookup("build")
	if f == nil {
		t.Fatal("sync: --build flag not found")
	}

	// --source must not exist.
	if syncCmd.Flags().Lookup("source") != nil {
		t.Error("sync: --source flag should not exist")
	}
}

func TestInstallBuildFlag(t *testing.T) {
	f := installCmd.Flags().Lookup("build")
	if f == nil {
		t.Fatal("install: --build flag not found")
	}
}

func TestUpdateBuildFlag(t *testing.T) {
	f := updateCmd.Flags().Lookup("build")
	if f == nil {
		t.Fatal("update: --build flag not found")
	}
}

// A worker's integrity failure must keep its exit code. reportSyncOutcomes
// used to reduce every outcome to a count, and finishSync then built a
// fresh error from that count, so the sentinel died at the barrier: a
// cached provenance conflict — the single most important thing #182
// detects — exited 1 instead of 3.
//
// The integration script did not catch this, and could not: its failure
// happens during plan construction, which returns straight out of
// runSync without passing through a worker at all.
func TestFinishSyncPreservesAWorkerIntegrityFailure(t *testing.T) {
	conflict := fmt.Errorf("jq@1.7-1: %w", provenance.ErrInvalid)

	err := finishSync(syncFinish{
		failures: []error{conflict}, installed: 1, locked: true,
	}, func() error { return nil })

	if err == nil {
		t.Fatal("expected sync error")
	}
	if !errors.Is(err, provenance.ErrInvalid) {
		t.Errorf("sync error must wrap the worker's sentinel, got %v", err)
	}
	if got := exitCodeFor(err); got != exitLockIntegrity {
		t.Errorf("integrity failure: got exit %d, want %d",
			got, exitLockIntegrity)
	}
}

// The unlocked twin: an ordinary failure still classifies as one, so
// the wrapping does not promote every sync failure to an integrity
// violation.
func TestFinishSyncKeepsAnOrdinaryFailureOrdinary(t *testing.T) {
	err := finishSync(syncFinish{
		failures: []error{errors.New("connection refused")}, installed: 1,
	}, func() error { return nil })

	if err == nil {
		t.Fatal("expected sync error")
	}
	if got := exitCodeFor(err); got != exitFailure {
		t.Errorf("ordinary failure: got exit %d, want %d", got, exitFailure)
	}
}

// Acceptance 11 (design §8): under a lock, finishSync is skipped on
// ANY plan failure — no generation rebuild, no swap, even when the
// config changed. Issue #20's partial rebuild is the right trade
// unlocked, where a broken recipe should not cost the user their whole
// PATH. Under a lock it is the wrong one: the plan is a unit, and
// activating the part of it that happened to verify publishes a
// generation the lock never describes.
//
// "Any" is the load-bearing word. Restricting the skip to integrity
// failures would rebuild after a network error, which leaves exactly
// the same partial generation.
func TestFinishSyncSkipsRebuildUnderALockWithAnyFailure(t *testing.T) {
	called := false
	err := finishSync(syncFinish{
		failures: make([]error, 1), installed: 3, configChanged: true, locked: true,
	}, func() error {
		called = true
		return nil
	})
	if err == nil {
		t.Fatal("expected sync error")
	}
	if called {
		t.Error("locked sync with a failure must not rebuild the generation")
	}
}

// The unlocked twin, so the two behaviors are pinned against each
// other rather than one being asserted alone: the same failure still
// rebuilds per issue #20.
func TestFinishSyncStillRebuildsUnlockedWithAFailure(t *testing.T) {
	called := false
	err := finishSync(syncFinish{
		failures: make([]error, 1), installed: 3, configChanged: true, locked: false,
	}, func() error {
		called = true
		return nil
	})
	if err == nil {
		t.Fatal("expected sync error")
	}
	if !called {
		t.Error("unlocked sync must still rebuild (issue #20)")
	}
}

// A locked sync that fully succeeded rebuilds normally. The skip keys
// on failure, not on the lock: refusing to rebuild a clean locked sync
// would mean nothing a lock describes ever reaches PATH.
func TestFinishSyncRebuildsUnderALockWhenNothingFailed(t *testing.T) {
	called := false
	err := finishSync(syncFinish{
		installed: 2, locked: true,
	}, func() error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("clean locked sync: %v", err)
	}
	if !called {
		t.Error("clean locked sync must rebuild the generation")
	}
}

func TestFinishSyncRebuildsOnFailure(t *testing.T) {
	// Issue #20: rebuild even on partial failure so the
	// packages that did install land on PATH. The failure
	// error still propagates so the exit code is non-zero.
	called := false
	err := finishSync(syncFinish{failures: make([]error, 1)}, func() error {
		called = true
		return nil
	})
	if err == nil {
		t.Fatal("expected sync error")
	}
	if !called {
		t.Fatal("rebuild should be called even when sync had failures")
	}
}

func TestFinishSyncFailureErrorMentionsBothFailures(t *testing.T) {
	// When both an install failure and a rebuild error occur,
	// the returned error must mention both so neither is silently
	// discarded. The install count tells the user which package
	// broke; the rebuild error tells them the PATH may be stale.
	rebuildErr := errors.New("rebuild boom")
	err := finishSync(syncFinish{failures: make([]error, 2)}, func() error {
		return rebuildErr
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, rebuildErr) {
		t.Fatalf("error %q must wrap the rebuild error", err)
	}
}

func TestFinishSyncReturnsRebuildError(t *testing.T) {
	errBoom := errors.New("boom")
	err := finishSync(syncFinish{installed: 1}, func() error {
		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("finishSync error = %v, want %v", err, errBoom)
	}
}

// TestFinishSyncIncludesRebuildErrorOnFailure verifies that when
// both failed > 0 and rebuildErr != nil, finishSync wraps the
// rebuild error so callers can inspect it via errors.Is. Previously
// the rebuild error was silently discarded when failed > 0.
func TestFinishSyncIncludesRebuildErrorOnFailure(t *testing.T) {
	rebuildErr := errors.New("generation build failed")
	err := finishSync(syncFinish{failures: make([]error, 1)}, func() error { return rebuildErr })
	if err == nil {
		t.Fatal("finishSync must return error when failed > 0")
	}
	if !errors.Is(err, rebuildErr) {
		t.Errorf("finishSync error %q must wrap the rebuild error", err)
	}
}

func TestFinishSyncSkipsRebuildInDryRun(t *testing.T) {
	called := false
	err := finishSync(syncFinish{dryRun: true, installed: 1}, func() error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("finishSync error = %v, want nil", err)
	}
	if called {
		t.Fatal("rebuild should not be called in dry-run mode")
	}
}

func TestFinishSyncFailurePreservesPartialProgress(t *testing.T) {
	// Issue #20: when sync partially fails (one recipe broken,
	// others installed), the next generation should reflect
	// what's actually in the store. Packages whose install
	// succeeded land on PATH; packages that failed install are
	// absent from current/bin (populateGeneration skips
	// missing store entries). The error still propagates.
	storeRoot := t.TempDir()
	galeDir := filepath.Join(t.TempDir(), ".gale")
	configPath := filepath.Join(t.TempDir(), "gale.toml")

	// Only oldpkg is in the store — newpkg "failed" to install.
	s := store.NewStore(storeRoot)
	pkgDir, err := s.Create("oldpkg", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(pkgDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "oldpkg"),
		[]byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Config lists both — newpkg was requested but its
	// install failed, so it's not in the store.
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n  oldpkg = \"1.0.0\"\n  newpkg = \"1.0.0\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	err = finishSync(syncFinish{failures: make([]error, 1)}, func() error {
		return rebuildGeneration(galeDir, storeRoot, configPath, nil)
	})
	if err == nil {
		t.Fatal("expected sync error")
	}

	// current/bin must contain oldpkg (install succeeded).
	if _, err := os.Lstat(filepath.Join(galeDir, "current", "bin", "oldpkg")); err != nil {
		t.Fatalf("oldpkg missing after failed sync: %v", err)
	}
	// current/bin must NOT contain newpkg (install failed).
	if _, err := os.Lstat(filepath.Join(galeDir, "current", "bin", "newpkg")); !os.IsNotExist(err) {
		t.Fatalf("newpkg should not be active after failed sync, err=%v", err)
	}
}

func TestRunSyncProjectFlagAccepted(t *testing.T) {
	// Before the fix, syncProject was declared but never
	// passed to runSync. runSync only accepted 3 args
	// (recipesPath, buildOnly, global). This test verifies
	// that runSync accepts the project parameter.
	//
	// The test calls runSync with project=true. Before the
	// fix this would not compile. After the fix, the
	// project flag is passed through and honored.

	// Create a project directory with gale.toml.
	t.Setenv("HOME", t.TempDir()) // isolate ~/.gale (project registry)
	projDir := t.TempDir()
	projConfig := filepath.Join(projDir, "gale.toml")
	if err := os.WriteFile(projConfig,
		[]byte("[packages]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)
	os.Chdir(projDir)

	// This call verifies the function signature accepts
	// the project parameter. Before the fix, this would
	// fail to compile with "too many arguments".
	err = runSync("", false, false, true, "")
	// The sync itself may fail (no store, etc.) but the
	// important thing is that the function accepts 4 args
	// and the project flag reaches config resolution.
	_ = err
}

// TestFinishSyncSkipsRebuildWhenNothingInstalled guards against bug 0020:
// finishSync always calls rebuild() when not in dry-run mode, even when
// no packages were actually installed. This creates a new generation on
// every invocation, causing the generation counter to grow without bound.
// Fix: add an `installed int` parameter and skip rebuild when installed == 0.
func TestFinishSyncSkipsRebuildWhenNothingInstalled(t *testing.T) {
	rebuilt := false
	err := finishSync(syncFinish{}, func() error {
		rebuilt = true
		return nil
	})
	if err != nil {
		t.Fatalf("finishSync returned unexpected error: %v", err)
	}
	if rebuilt {
		t.Error("finishSync must not call rebuild when installed == 0")
	}
}

// TestFinishSyncRebuildsWhenConfigChanged pins the fix for the
// removed-symlink regression: when nothing needs (re)installing but
// gale.toml has dropped a package, sync must still rebuild so the
// stale symlink leaves current/bin. Skipping rebuild on
// installed == 0 was leaving the old generation active.
func TestFinishSyncRebuildsWhenConfigChanged(t *testing.T) {
	rebuilt := false
	err := finishSync(syncFinish{configChanged: true}, func() error {
		rebuilt = true
		return nil
	})
	if err != nil {
		t.Fatalf("finishSync returned unexpected error: %v", err)
	}
	if !rebuilt {
		t.Error("finishSync must rebuild when configChanged is true")
	}
}

// TestFinishSyncDropsRemovedPackageSymlink is the behavioural
// pin for the sync_cleans_removed_symlink regression. After a
// package is removed from gale.toml and sync runs with nothing
// to install, the new generation must omit the removed
// package's symlink.
func TestFinishSyncDropsRemovedPackageSymlink(t *testing.T) {
	storeRoot := t.TempDir()
	galeDir := filepath.Join(t.TempDir(), ".gale")
	configPath := filepath.Join(t.TempDir(), "gale.toml")

	s := store.NewStore(storeRoot)
	for _, name := range []string{"keep", "drop"} {
		pkgDir, err := s.Create(name, "1.0.0")
		if err != nil {
			t.Fatal(err)
		}
		binDir := filepath.Join(pkgDir, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(binDir, name),
			[]byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Initial config: both packages. Build the generation so
	// drop's symlink lands in current/bin.
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n  keep = \"1.0.0\"\n  drop = \"1.0.0\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	if err := rebuildGeneration(galeDir, storeRoot, configPath, nil); err != nil {
		t.Fatalf("initial rebuild: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(galeDir, "current", "bin", "drop")); err != nil {
		t.Fatalf("drop symlink missing before removal: %v", err)
	}

	// Hand-edit config to remove drop, then drive finishSync as
	// runSync would: nothing was installed, nothing failed, but
	// the config no longer matches the active generation.
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n  keep = \"1.0.0\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	err := finishSync(syncFinish{configChanged: true}, func() error {
		return rebuildGeneration(galeDir, storeRoot, configPath, nil)
	})
	if err != nil {
		t.Fatalf("finishSync after config edit: %v", err)
	}

	if _, err := os.Lstat(filepath.Join(galeDir, "current", "bin", "drop")); !os.IsNotExist(err) {
		t.Fatalf("drop symlink must be gone after sync; err=%v", err)
	}
	if _, err := os.Lstat(filepath.Join(galeDir, "current", "bin", "keep")); err != nil {
		t.Fatalf("keep symlink must remain: %v", err)
	}
}

// TestSyncDriftedTrueWhenFarmIsMissingADepDylib pins that gale sync
// rebuilds when the generation already matches and only the shared
// farm is wrong. finishSync skips rebuild when installed == 0 and
// configChanged is false; configChanged used to mean generation
// package drift only. Farm drift is exactly the case doctor --repair
// used to fix, and "Run: gale sync" is a no-op without this.
func TestSyncDriftedTrueWhenFarmIsMissingADepDylib(t *testing.T) {
	dylib := versionedDylibName(t)
	home := t.TempDir()
	galeDir := filepath.Join(home, ".gale")
	storeRoot := filepath.Join(galeDir, "pkg")

	fakelibStore(t, storeRoot, dylib)
	appDir := filepath.Join(storeRoot, "app", "1.0.0-1")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := depsmeta.Write(appDir, depsmeta.Metadata{Deps: []depsmeta.ResolvedDep{
		{Name: "fakelib", Version: "1.0.0", Revision: 1},
	}}); err != nil {
		t.Fatal(err)
	}

	pkgs := map[string]string{"app": "1.0.0"}
	if err := generation.Build(pkgs, galeDir, storeRoot); err != nil {
		t.Fatal(err)
	}
	if generationDrifted(galeDir, storeRoot, pkgs, nil) {
		t.Fatal("generation must match so only the farm is wrong")
	}

	farmDir := filepath.Join(home, ".gale", "lib")
	if entries, err := os.ReadDir(farmDir); err != nil {
		t.Fatalf("farm after Build: %v", err)
	} else if len(entries) == 0 {
		t.Fatal("Build must have populated the farm so wiping it is drift")
	}
	if err := os.RemoveAll(farmDir); err != nil {
		t.Fatal(err)
	}

	if !syncDrifted(driftQuery{
		galeDir:   galeDir,
		storeRoot: storeRoot,
		declared:  pkgs,
	}) {
		t.Fatal("syncDrifted must be true when the farm is missing a dep dylib")
	}
}

// TestSyncDriftedTrueWhenLockedFarmDiffersFromHighestRevision pins
// that a locked sync checks farm closure against the lock, not
// gale.toml's bare pins. FarmStoreDirs floats a bare pin to the
// highest on-disk revision. An orphan higher revision whose
// closure needs no farm entries would hide farm drift for the
// locked generation, and finishSync would skip the rebuild that
// replaced doctor --repair.
func TestSyncDriftedTrueWhenLockedFarmDiffersFromHighestRevision(t *testing.T) {
	dylib := versionedDylibName(t)
	home := t.TempDir()
	galeDir := filepath.Join(home, ".gale")
	storeRoot := filepath.Join(galeDir, "pkg")

	fakelibStore(t, storeRoot, dylib)
	lockedDir := filepath.Join(storeRoot, "app", "1.0.0-1")
	if err := os.MkdirAll(lockedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := depsmeta.Write(lockedDir, depsmeta.Metadata{Deps: []depsmeta.ResolvedDep{
		{Name: "fakelib", Version: "1.0.0", Revision: 1},
	}}); err != nil {
		t.Fatal(err)
	}

	// Populated leaf: empty in-flight dirs are skipped (gh#76),
	// so a bare pin would otherwise still resolve to 1.0.0-1.
	orphan := seedStore(t, storeRoot, "app", "1.0.0-2")
	if err := depsmeta.Write(orphan, depsmeta.Metadata{}); err != nil {
		t.Fatal(err)
	}

	if err := generation.Build(
		map[string]string{"app": "1.0.0-1"}, galeDir, storeRoot,
	); err != nil {
		t.Fatal(err)
	}

	farmDir := filepath.Join(home, ".gale", "lib")
	if err := os.RemoveAll(farmDir); err != nil {
		t.Fatal(err)
	}

	plan := &lockplan.Plan{
		Nodes: map[string]lockplan.Node{
			"app@1.0.0-1": {Name: "app", Version: "1.0.0-1"},
		},
		Order: []string{"app@1.0.0-1"},
		Roots: []string{"app@1.0.0-1"},
	}
	if lockedGenerationDrifted(galeDir, storeRoot, plan) {
		t.Fatal("generation must match the lock so only the farm is wrong")
	}

	if !syncDrifted(driftQuery{
		galeDir:   galeDir,
		storeRoot: storeRoot,
		declared:  map[string]string{"app": "1.0.0"},
		plan:      plan,
	}) {
		t.Fatal("syncDrifted must be true when the locked generation's farm is missing, even if a higher orphan revision needs no farm entries")
	}
}

// NOTE (finding 0005): The bug where sync --dry-run emits "stale —
// reinstalling" before the dry-run check cannot be unit-tested without
// output-capture infrastructure (newOutput() writes directly to os.Stderr).

// NOTE (findings 0006 and 0008 — sync's lockfile write): both
// described bugs in the per-package lockfile write sync used to
// perform after each install. Sync no longer writes gale.lock at all
// (design §11), so the line both fixes landed on is gone. The
// canonical-version invariant they were about lives on in the lock
// writers, which root r.Package.Full() and nothing else.
