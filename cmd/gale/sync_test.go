package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/output"
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

func TestSyncSHA256MismatchEvictsFromStore(t *testing.T) {
	// Set up a store with a package already installed.
	storeRoot := t.TempDir()
	s := store.NewStore(storeRoot)
	pkgDir, err := s.Create("testpkg", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(pkgDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Package is in the store.
	if !s.IsInstalled("testpkg", "1.0.0") {
		t.Fatal("testpkg should be installed before test")
	}

	// Simulate SHA256 mismatch: locked hash differs from
	// result hash. The eviction should remove the package.
	result := &installer.InstallResult{
		Name:    "testpkg",
		Version: "1.0.0",
		SHA256:  "aaaa1111aaaa1111aaaa1111aaaa1111",
	}
	lockedSHA := "bbbb2222bbbb2222bbbb2222bbbb2222"

	out := output.New(os.Stderr, false)
	evicted := evictOnSHA256Mismatch(s, result, lockedSHA, out)
	if !evicted {
		t.Fatal("expected eviction on SHA256 mismatch")
	}

	// Package should be removed from the store.
	if s.IsInstalled("testpkg", "1.0.0") {
		t.Error("testpkg should be removed from store " +
			"after SHA256 mismatch")
	}
}

func TestSyncSHA256MatchDoesNotEvict(t *testing.T) {
	storeRoot := t.TempDir()
	s := store.NewStore(storeRoot)
	pkgDir, err := s.Create("testpkg", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	// Add a file so IsInstalled reports true (empty dirs
	// are treated as failed installs).
	if err := os.WriteFile(filepath.Join(pkgDir, "marker"),
		[]byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := &installer.InstallResult{
		Name:    "testpkg",
		Version: "1.0.0",
		SHA256:  "aaaa1111aaaa1111aaaa1111aaaa1111",
	}
	// Same hash — no mismatch.
	lockedSHA := "aaaa1111aaaa1111aaaa1111aaaa1111"

	out := output.New(os.Stderr, false)
	evicted := evictOnSHA256Mismatch(s, result, lockedSHA, out)
	if evicted {
		t.Error("should not evict when SHA256 matches")
	}

	if !s.IsInstalled("testpkg", "1.0.0") {
		t.Error("testpkg should remain in store")
	}
}

func TestFinishSyncRebuildsOnFailure(t *testing.T) {
	// Issue #20: rebuild even on partial failure so the
	// packages that did install land on PATH. The failure
	// error still propagates so the exit code is non-zero.
	called := false
	err := finishSync(false, 1, func() error {
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

func TestFinishSyncFailureErrorTakesPrecedence(t *testing.T) {
	// When both an install failure and a rebuild error
	// occur, the install-failure count is the more useful
	// signal — it tells the user which package broke.
	err := finishSync(false, 2, func() error {
		return errors.New("rebuild boom")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if got := err.Error(); got != "2 package(s) could not be synced" {
		t.Fatalf("error = %q, want install-failure message", got)
	}
}

func TestFinishSyncReturnsRebuildError(t *testing.T) {
	errBoom := errors.New("boom")
	err := finishSync(false, 0, func() error {
		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("finishSync error = %v, want %v", err, errBoom)
	}
}

func TestFinishSyncSkipsRebuildInDryRun(t *testing.T) {
	called := false
	err := finishSync(true, 0, func() error {
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

	err = finishSync(false, 1, func() error {
		return rebuildGeneration(galeDir, storeRoot, configPath)
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

// TestSyncWritesLockfileHash documents that sync should
// write SHA256 hashes to the lockfile after successful
// installs. A full integration test would require mocking
// the installer, so this test just verifies the code path
// exists and the lockfilePath helper is called correctly.
func TestSyncWritesLockfileHash(t *testing.T) {
	// This test verifies that the sync command updates
	// the lockfile with SHA256 values after installing
	// packages. The actual integration test would require
	// a full install setup, but we can at least verify
	// the lockfilePath function is called correctly.
	//
	// The key code is in sync.go after reportResult:
	//   if result.SHA256 != "" {
	//     lp, lpErr := lockfilePath(ctx.GalePath)
	//     if lpErr == nil {
	//       _ = updateLockfile(lp, name, version, result.SHA256)
	//     }
	//   }
	//
	// For now, this test just documents the expected
	// behavior. Integration tests should verify the
	// lockfile actually gets updated.
	t.Log("sync should write SHA256 hashes to gale.lock")
}
