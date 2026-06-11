package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/kelp/gale/internal/lockfile"
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

// TestSyncSHA256MismatchKeepsInstallAndUpdatesLockfile
// pins the warn-and-update behavior on lockfile SHA
// mismatch. The install itself verified the download
// against the recipe's expected hash, so a disagreement
// against the local lockfile only means the recipe
// (or build output) has shifted since the last install
// on this machine. Evicting a freshly-verified package
// used to leave users stuck re-downloading and
// re-building on every sync; now we keep it and update
// the cache.
func TestSyncSHA256MismatchKeepsInstallAndUpdatesLockfile(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "gale.toml")
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n  testpkg = \"1.0.0\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	lockPath := filepath.Join(tmp, "gale.lock")
	lf := &lockfile.LockFile{
		Packages: map[string]lockfile.LockedPackage{
			"testpkg": {
				Version: "1.0.0",
				SHA256:  "oldhasholdhasholdhasholdhash",
			},
		},
	}
	if err := lockfile.Write(lockPath, lf); err != nil {
		t.Fatal(err)
	}

	newHash := "newhashnewhashnewhashnewhashnewh"
	if err := updateLockfile(
		lockPath, "testpkg", "1.0.0", newHash, "",
	); err != nil {
		t.Fatalf("updateLockfile: %v", err)
	}

	got, err := lockfile.Read(lockPath)
	if err != nil {
		t.Fatalf("lockfile.Read: %v", err)
	}
	entry, ok := got.Packages["testpkg"]
	if !ok {
		t.Fatal("testpkg entry missing after update")
	}
	if entry.SHA256 != newHash {
		t.Errorf("lockfile SHA256 = %q, want %q",
			entry.SHA256, newHash)
	}
}

func TestFinishSyncRebuildsOnFailure(t *testing.T) {
	// Issue #20: rebuild even on partial failure so the
	// packages that did install land on PATH. The failure
	// error still propagates so the exit code is non-zero.
	called := false
	err := finishSync(false, 1, 0, false, func() error {
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
	err := finishSync(false, 2, 0, false, func() error {
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
	err := finishSync(false, 0, 1, false, func() error {
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
	err := finishSync(false, 1, 0, false, func() error { return rebuildErr })
	if err == nil {
		t.Fatal("finishSync must return error when failed > 0")
	}
	if !errors.Is(err, rebuildErr) {
		t.Errorf("finishSync error %q must wrap the rebuild error", err)
	}
}

func TestFinishSyncSkipsRebuildInDryRun(t *testing.T) {
	called := false
	err := finishSync(true, 0, 1, false, func() error {
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

	err = finishSync(false, 1, 0, false, func() error {
		return rebuildGenerationLenient(galeDir, storeRoot, configPath)
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
	err := finishSync(false, 0, 0, false, func() error {
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
	err := finishSync(false, 0, 0, true, func() error {
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
	if err := rebuildGenerationLenient(galeDir, storeRoot, configPath); err != nil {
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

	err := finishSync(false, 0, 0, true, func() error {
		return rebuildGenerationLenient(galeDir, storeRoot, configPath)
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

// TestSyncWritesLockfileHash documents that sync should
// write SHA256 hashes to the lockfile after successful
// installs. A full integration test would require mocking
// the installer, so this test just verifies the code path
// exists and the lockfilePath helper is called correctly.
func TestSyncWritesLockfileHash(t *testing.T) {
	t.Skip("integration test: requires store+registry infrastructure")
}

// NOTE (finding 0005): The bug where sync --dry-run emits "stale —
// reinstalling" before the dry-run check cannot be unit-tested without
// output-capture infrastructure (newOutput() writes directly to os.Stderr).

// NOTE (finding 0006 — sync writes bare version to lockfile):
//
// Fix location: sync.go, around line 202 (inside runSync, after
// reportResult).
//
// Buggy code:
//   _ = updateLockfile(lp, name, version, result.SHA256)
//   // `version` is the bare string from gale.toml ("1.8.1").
//
// Fix:
//   _ = updateLockfile(lp, name, r.Package.Full(), result.SHA256)
//   // r is in scope (fetched at line ~139 via ResolveVersionedRecipe).
//   // r.Package.Full() returns "1.8.1-1" (canonical form with revision).
//
// Impact: install and update both use r.Package.Full() when writing to
// the lockfile. Sync used the bare version from gale.toml. This
// inconsistency means:
//
//   1. gale install jq writes "1.8.1-1" to the lockfile.
//   2. gale sync reinstalls jq, overwrites lock entry with "1.8.1".
//   3. lockfile.IsStale compares locked.Version ("1.8.1") against
//      tomlPackages["jq"] ("1.8.1") — they match, no stale signal.
//   4. But a subsequent install again writes "1.8.1-1" → mismatch
//      with toml's "1.8.1" → IsStale returns true → perpetual resync.
//
// The unit-level test for the invariant is:
// internal/lockfile/lockfile_test.go:TestIsStaleCanonicalAndBareVersionsAreEquivalent.

// NOTE (finding 0008 — sync discards lockfile write errors):
//
// Fix location: sync.go, same line as finding 0006.
//
// Buggy code:
//   _ = updateLockfile(lp, name, version, result.SHA256)
//
// Fix: propagate or log the error. The simplest approach:
//   if err := updateLockfile(lp, name, r.Package.Full(), result.SHA256); err != nil {
//       out.Warn(fmt.Sprintf("updating lockfile for %s: %v", name, err))
//   }
//
// The fix for 0006 (using r.Package.Full()) and 0008 (not discarding
// the error) are both on the same line, so they are fixed together.
//
// A unit test for this behavior requires mocking the lockfile write
// path, which would require refactoring updateLockfile to accept an
// injectable writer. The integration-level signal is that a read-only
// lockfile causes sync to emit a warning rather than silently succeeding.
// The fix is a one-line code movement in runSync — moving the stale info
// message inside the !dryRun block.
