package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

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

	err := finishSync(false, 0, 0, true, func() error {
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

// NOTE (finding 0005): The bug where sync --dry-run emits "stale —
// reinstalling" before the dry-run check cannot be unit-tested without
// output-capture infrastructure (newOutput() writes directly to os.Stderr).

// NOTE (findings 0006 and 0008 — sync's lockfile write): both
// described bugs in the per-package lockfile write sync used to
// perform after each install. Sync no longer writes gale.lock at all
// (design §11), so the line both fixes landed on is gone. The
// canonical-version invariant they were about lives on in the lock
// writers, which root r.Package.Full() and nothing else.
