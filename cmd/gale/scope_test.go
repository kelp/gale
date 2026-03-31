package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kelp/gale/internal/store"
)

// TestRebuildGenerationProjectDoesNotTouchGlobal verifies
// that rebuilding a project generation only creates
// symlinks in the project's .gale/current, not the global
// ~/.gale/current.
func TestRebuildGenerationProjectDoesNotTouchGlobal(t *testing.T) {
	// Set up a fake store with one package.
	storeRoot := t.TempDir()
	s := store.NewStore(storeRoot)
	pkgDir, err := s.Create("testpkg", "1.0")
	if err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(pkgDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(binDir, "testpkg"),
		[]byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a "global" gale dir with an empty config.
	globalDir := filepath.Join(t.TempDir(), "global-gale")
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	globalConfig := filepath.Join(globalDir, "gale.toml")
	if err := os.WriteFile(globalConfig,
		[]byte("[packages]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Build an empty global generation.
	if err := rebuildGeneration(
		globalDir, storeRoot, globalConfig); err != nil {
		t.Fatalf("rebuild global: %v", err)
	}

	// Create a project dir with testpkg declared.
	projRoot := t.TempDir()
	projConfig := filepath.Join(projRoot, "gale.toml")
	if err := os.WriteFile(projConfig,
		[]byte("[packages]\n  testpkg = \"1.0\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	projGaleDir := filepath.Join(projRoot, ".gale")

	// Build project generation.
	if err := rebuildGeneration(
		projGaleDir, storeRoot, projConfig); err != nil {
		t.Fatalf("rebuild project: %v", err)
	}

	// Project should have testpkg.
	projBin := filepath.Join(
		projGaleDir, "current", "bin", "testpkg")
	if _, err := os.Lstat(projBin); err != nil {
		t.Errorf("project should have testpkg: %v", err)
	}

	// Global should NOT have testpkg.
	globalBin := filepath.Join(
		globalDir, "current", "bin", "testpkg")
	if _, err := os.Lstat(globalBin); err == nil {
		t.Error("global should not have testpkg")
	}
}

// TestRebuildGenerationGlobalDoesNotTouchProject is the
// inverse: rebuilding global doesn't affect an existing
// project generation.
func TestRebuildGenerationGlobalDoesNotTouchProject(t *testing.T) {
	storeRoot := t.TempDir()
	s := store.NewStore(storeRoot)

	// Create two packages in the store.
	for _, name := range []string{"globalpkg", "projpkg"} {
		pkgDir, err := s.Create(name, "1.0")
		if err != nil {
			t.Fatal(err)
		}
		binDir := filepath.Join(pkgDir, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(binDir, name),
			[]byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Build project generation first.
	projRoot := t.TempDir()
	projConfig := filepath.Join(projRoot, "gale.toml")
	if err := os.WriteFile(projConfig,
		[]byte("[packages]\n  projpkg = \"1.0\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	projGaleDir := filepath.Join(projRoot, ".gale")
	if err := rebuildGeneration(
		projGaleDir, storeRoot, projConfig); err != nil {
		t.Fatal(err)
	}

	// Verify project has projpkg.
	projBin := filepath.Join(
		projGaleDir, "current", "bin", "projpkg")
	if _, err := os.Lstat(projBin); err != nil {
		t.Fatalf("project should have projpkg: %v", err)
	}

	// Now rebuild global generation.
	globalDir := filepath.Join(t.TempDir(), "global-gale")
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	globalConfig := filepath.Join(globalDir, "gale.toml")
	if err := os.WriteFile(globalConfig,
		[]byte("[packages]\n  globalpkg = \"1.0\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	if err := rebuildGeneration(
		globalDir, storeRoot, globalConfig); err != nil {
		t.Fatal(err)
	}

	// Project generation should still have projpkg.
	if _, err := os.Lstat(projBin); err != nil {
		t.Error("global rebuild broke project generation")
	}

	// Project should NOT have globalpkg.
	projGlobal := filepath.Join(
		projGaleDir, "current", "bin", "globalpkg")
	if _, err := os.Lstat(projGlobal); err == nil {
		t.Error("project should not have globalpkg")
	}
}
