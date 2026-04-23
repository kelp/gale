package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/registry"
	"github.com/kelp/gale/internal/store"
)

func TestLockfilePathWithTomlSuffix(t *testing.T) {
	got, err := lockfilePath("/home/user/.gale/gale.toml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "/home/user/.gale/gale.lock"
	if got != want {
		t.Errorf("lockfilePath() = %q, want %q", got, want)
	}
}

func TestLockfilePathReturnsErrorForNonToml(t *testing.T) {
	_, err := lockfilePath("/home/user/.gale/gale.conf")
	if err == nil {
		t.Fatal("expected error for path without .toml suffix")
	}
	if !strings.Contains(err.Error(), ".toml") {
		t.Errorf("error message %q should mention .toml", err)
	}
}

func TestLockfilePathReturnsCorrectPath(t *testing.T) {
	got, err := lockfilePath("/tmp/gale.toml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "/tmp/gale.lock"
	if got != want {
		t.Errorf("lockfilePath() = %q, want %q", got, want)
	}
}

func TestWriteConfigAndLockUpdatesLockfileOnCachedInstall(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "gale.toml")
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n  mypkg = \"1.0.0\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	// Write an initial lockfile with v1.0.0 and a hash.
	lockPath := filepath.Join(tmp, "gale.lock")
	lf := &lockfile.LockFile{
		Packages: map[string]lockfile.LockedPackage{
			"mypkg": {Version: "1.0.0", SHA256: "oldhash123"},
		},
	}
	if err := lockfile.Write(lockPath, lf); err != nil {
		t.Fatal(err)
	}

	// Simulate a cached install to v2.0.0 (sha256 is empty).
	if err := writeConfigAndLock(
		configPath, "mypkg", "2.0.0", "2.0.0", ""); err != nil {
		t.Fatalf("writeConfigAndLock: %v", err)
	}

	// Read the lockfile back. The version should be updated
	// even though sha256 was empty. The old hash must not
	// remain associated with the new version.
	got, err := lockfile.Read(lockPath)
	if err != nil {
		t.Fatalf("reading lockfile: %v", err)
	}

	pkg, ok := got.Packages["mypkg"]
	if !ok {
		t.Fatal("mypkg not found in lockfile")
	}
	if pkg.Version != "2.0.0" {
		t.Errorf("lockfile version = %q, want %q",
			pkg.Version, "2.0.0")
	}
	if pkg.SHA256 == "oldhash123" {
		t.Error("lockfile still has old hash from v1.0.0")
	}
}

func TestWriteConfigAndLockPreservesHashOnSameVersionCache(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "gale.toml")
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n  mypkg = \"1.0.0\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	// Write a lockfile with v1.0.0 and a valid hash.
	lockPath := filepath.Join(tmp, "gale.lock")
	lf := &lockfile.LockFile{
		Packages: map[string]lockfile.LockedPackage{
			"mypkg": {Version: "1.0.0", SHA256: "validhash"},
		},
	}
	if err := lockfile.Write(lockPath, lf); err != nil {
		t.Fatal(err)
	}

	// Cached install of the same version (sha256 empty).
	if err := writeConfigAndLock(
		configPath, "mypkg", "1.0.0", "1.0.0", ""); err != nil {
		t.Fatalf("writeConfigAndLock: %v", err)
	}

	// The existing hash should be preserved.
	got, err := lockfile.Read(lockPath)
	if err != nil {
		t.Fatalf("reading lockfile: %v", err)
	}
	pkg := got.Packages["mypkg"]
	if pkg.SHA256 != "validhash" {
		t.Errorf("lockfile hash = %q, want %q",
			pkg.SHA256, "validhash")
	}
}

// H5: finalizeInstall uses the strict (Build) rebuild path.
// A configured package whose store dir is missing surfaces
// as an error — the user learns about the corruption
// instead of getting a silently-incomplete generation.
func TestFinalizeInstallErrorsOnMissingConfiguredPackage(t *testing.T) {
	tmp := t.TempDir()
	galeDir := filepath.Join(tmp, ".gale")
	storeRoot := filepath.Join(tmp, "pkg")
	configPath := filepath.Join(tmp, "gale.toml")
	lockPath := filepath.Join(tmp, "gale.lock")

	if err := os.WriteFile(configPath,
		[]byte("[packages]\n  awscli = \"2.34.19\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.Write(lockPath, &lockfile.LockFile{
		Packages: map[string]lockfile.LockedPackage{
			"awscli": {Version: "2.34.19", SHA256: "oldhash"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	s := store.NewStore(storeRoot)
	pkgDir, err := s.Create("gale", "0.11.1")
	if err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(pkgDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "gale"),
		[]byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	err = finalizeInstall(galeDir, storeRoot, configPath,
		"gale", "0.11.1", "0.11.1", "newhash")
	if err == nil {
		t.Fatal("expected finalizeInstall error for missing awscli store dir")
	}
	if !strings.Contains(err.Error(), "awscli") {
		t.Errorf("error %q does not mention awscli", err)
	}
}

func TestFinalizeInstallRebuildFailureKeepsCurrent(t *testing.T) {
	tmp := t.TempDir()
	galeDir := filepath.Join(tmp, ".gale")
	storeRoot := filepath.Join(tmp, "pkg")
	configPath := filepath.Join(tmp, "gale.toml")

	s := store.NewStore(storeRoot)
	for _, pkg := range []struct {
		name    string
		version string
	}{
		{name: "oldpkg", version: "1.0.0"},
		{name: "newpkg", version: "2.0.0"},
	} {
		pkgDir, err := s.Create(pkg.name, pkg.version)
		if err != nil {
			t.Fatal(err)
		}
		binDir := filepath.Join(pkgDir, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(binDir, pkg.name),
			[]byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.WriteFile(configPath,
		[]byte("[packages]\n  oldpkg = \"1.0.0\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	if err := rebuildGeneration(galeDir, storeRoot, configPath); err != nil {
		t.Fatalf("initial rebuild: %v", err)
	}

	before, err := filepath.EvalSymlinks(filepath.Join(galeDir, "current"))
	if err != nil {
		t.Fatalf("eval current before: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(galeDir, "current", "bin", "oldpkg")); err != nil {
		t.Fatalf("oldpkg missing before finalizeInstall: %v", err)
	}

	if err := os.WriteFile(configPath,
		[]byte("[packages]\n  oldpkg = \"1.0.0\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(galeDir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(galeDir, 0o755)

	err = finalizeInstall(galeDir, storeRoot, configPath,
		"newpkg", "2.0.0", "2.0.0", "newhash")
	if err == nil {
		t.Fatal("expected finalizeInstall error")
	}

	after, err := filepath.EvalSymlinks(filepath.Join(galeDir, "current"))
	if err != nil {
		t.Fatalf("eval current after: %v", err)
	}
	if after != before {
		t.Fatalf("current changed on rebuild failure: before=%q after=%q", before, after)
	}
	if _, err := os.Lstat(filepath.Join(galeDir, "current", "bin", "oldpkg")); err != nil {
		t.Fatalf("oldpkg missing after failed finalizeInstall: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(galeDir, "current", "bin", "newpkg")); !os.IsNotExist(err) {
		t.Fatalf("newpkg should not be active after failed finalizeInstall, err=%v", err)
	}
}

func TestRebuildGenerationUsesToolVersionsFallback(t *testing.T) {
	projectDir := t.TempDir()
	galeDir := filepath.Join(projectDir, ".gale")
	storeRoot := filepath.Join(t.TempDir(), "pkg")
	configPath := filepath.Join(projectDir, "gale.toml")

	if err := os.WriteFile(filepath.Join(projectDir, ".tool-versions"),
		[]byte("golang 1.26.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := store.NewStore(storeRoot)
	pkgDir, err := s.Create("go", "1.26.1")
	if err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(pkgDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "go"),
		[]byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := rebuildGeneration(galeDir, storeRoot, configPath); err != nil {
		t.Fatalf("rebuildGeneration: %v", err)
	}

	if _, err := os.Lstat(filepath.Join(galeDir, "current", "bin", "go")); err != nil {
		t.Fatalf("go symlink missing from current generation: %v", err)
	}
}

// TestResolveVersionedRecipeMatchesFullVersion guards against a
// regression where the resolver-already-correct recipe was
// discarded because the equality check ignored the revision.
// Asking for "0.12.3-1" against a recipe whose Version is
// "0.12.3" and Revision is 1 must short-circuit on Full() and
// return that recipe.
func TestResolveVersionedRecipeMatchesFullVersion(t *testing.T) {
	want := &recipe.Recipe{
		Package: recipe.Package{
			Name:     "gale",
			Version:  "0.12.3",
			Revision: 1,
		},
	}
	ctx := &cmdContext{
		Resolver: func(name string) (*recipe.Recipe, error) {
			return want, nil
		},
	}
	got, err := resolveVersionedRecipe(ctx, "gale", "0.12.3-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Fatalf("resolveVersionedRecipe = %p, want %p", got, want)
	}
}

// TestResolveVersionedRecipeWrapsRegistryError guards against a
// regression where a real registry failure (404, signature
// failure, network error) was hidden behind the misleading
// "not found (registry has X)" string. The wrapped error must
// carry enough signal to diagnose the underlying cause.
func TestResolveVersionedRecipeWrapsRegistryError(t *testing.T) {
	want := &recipe.Recipe{
		Package: recipe.Package{
			Name:     "atuin",
			Version:  "18.13.6",
			Revision: 1,
		},
	}
	// Closed server → FetchRecipeVersion returns a connection error.
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := srv.URL
	srv.Close()
	reg := registry.NewWithURL(addr)
	ctx := &cmdContext{
		Resolver: func(name string) (*recipe.Recipe, error) {
			return want, nil
		},
		Registry: reg,
	}
	_, err := resolveVersionedRecipe(ctx, "atuin", "18.13.6-2")
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if strings.Contains(msg, "not found (registry has 18.13.6)") &&
		!strings.Contains(msg, "fetch") &&
		!strings.Contains(msg, "version index") &&
		!strings.Contains(msg, "connection refused") {
		t.Errorf("error %q hides the underlying registry failure",
			msg)
	}
}
