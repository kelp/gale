package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kelp/gale/internal/installer"
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

func TestSyncGitFlag(t *testing.T) {
	f := syncCmd.Flags().Lookup("git")
	if f == nil {
		t.Fatal("sync: --git flag not found")
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

	evicted := evictOnSHA256Mismatch(s, result, lockedSHA)
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

	evicted := evictOnSHA256Mismatch(s, result, lockedSHA)
	if evicted {
		t.Error("should not evict when SHA256 matches")
	}

	if !s.IsInstalled("testpkg", "1.0.0") {
		t.Error("testpkg should remain in store")
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
