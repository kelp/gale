package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/config"
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

// TestWriteConfigWritesToHostSection verifies that passing a
// non-empty host writes the package to [hosts.<host>.packages]
// rather than shared [packages]. This is the foundation that backs
// `gale install --host`.
func TestWriteConfigWritesToHostSection(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "gale.toml")
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := &cmdContext{GalePath: configPath, Host: "myhost"}
	if err := ctx.WriteConfig("mypkg", "1.0.0", "1.0.0-1"); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	if got := ctx.lockTargets["myhost"]["mypkg"]; got != "1.0.0-1" {
		t.Errorf("recorded lock root = %q, want the host target to root "+
			"mypkg at 1.0.0-1; targets = %v", got, ctx.lockTargets)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.ParseGaleConfig(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if _, in := cfg.Packages["mypkg"]; in {
		t.Errorf("mypkg leaked into shared [packages]: %q",
			string(data))
	}
	h, ok := cfg.Hosts["myhost"]
	if !ok {
		t.Fatalf("no [hosts.myhost] section: %q", string(data))
	}
	if got := h.Packages["mypkg"]; got != "1.0.0" {
		t.Errorf("hosts.myhost.packages[mypkg] = %q, want %q",
			got, "1.0.0")
	}
}

// TestWriteConfigHostUpdatesExisting verifies that reinstalling a
// host-scoped package with --host updates the version in place
// rather than duplicating into [packages].
func TestWriteConfigHostUpdatesExisting(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "gale.toml")
	initial := "[hosts.myhost.packages]\n  mypkg = \"1.0.0\"\n"
	if err := os.WriteFile(configPath,
		[]byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := &cmdContext{GalePath: configPath, Host: "myhost"}
	if err := ctx.WriteConfig("mypkg", "2.0.0", "2.0.0-1"); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.ParseGaleConfig(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if _, in := cfg.Packages["mypkg"]; in {
		t.Errorf("mypkg leaked into shared [packages]: %q",
			string(data))
	}
	if got := cfg.Hosts["myhost"].Packages["mypkg"]; got != "2.0.0" {
		t.Errorf("hosts.myhost.packages[mypkg] = %q, want %q",
			got, "2.0.0")
	}
}

func TestFinalizeInstallErrorsWhenTargetMissing(t *testing.T) {
	tmp := t.TempDir()
	galeDir := filepath.Join(tmp, ".gale")
	storeRoot := filepath.Join(tmp, "pkg")
	configPath := filepath.Join(tmp, "gale.toml")

	// The installed revision is in the store with provenance, so the
	// lock write can describe what was verified, but the config pin
	// below resolves to a version that is not on disk, so the
	// generation skips it — the same observable as the
	// race-or-deletion scenario the contract check guards against.
	// An absent store dir would fail the lock write first, for
	// another reason, and stop testing this one.
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeProvenance(t, storeRoot, "gale", "0.11.1-1")

	ctx := &cmdContext{
		GaleDir:   galeDir,
		StoreRoot: storeRoot,
		GalePath:  configPath,
	}
	err := ctx.FinalizeInstall("gale", "9.9.9", "0.11.1-1")
	if err == nil {
		t.Fatal("expected FinalizeInstall error for missing target store dir")
	}
	if !strings.Contains(err.Error(), "gale") {
		t.Errorf("error %q does not mention the target package", err)
	}
}

func TestFinalizeInstallRebuildFailureKeepsCurrent(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: read-only dirs are still writable")
	}
	tmp := t.TempDir()
	galeDir := filepath.Join(tmp, ".gale")
	storeRoot := filepath.Join(tmp, "pkg")
	configPath := filepath.Join(tmp, "gale.toml")

	s := store.NewStore(storeRoot)
	for _, pkg := range []struct {
		name    string
		version string
	}{
		{name: "oldpkg", version: "1.0.0-1"},
		{name: "newpkg", version: "2.0.0-1"},
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
	if err := rebuildGeneration(galeDir, storeRoot, configPath, nil); err != nil {
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

	ctx := &cmdContext{
		GaleDir:   galeDir,
		StoreRoot: storeRoot,
		GalePath:  configPath,
	}
	writeProvenance(t, storeRoot, "newpkg", "2.0.0-1")
	err = ctx.FinalizeInstall("newpkg", "2.0.0", "2.0.0-1")
	if err == nil {
		t.Fatal("expected FinalizeInstall error")
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

func TestRebuildGenerationIgnoresToolVersions(t *testing.T) {
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

	if err := rebuildGeneration(galeDir, storeRoot, configPath, nil); err != nil {
		t.Fatalf("rebuildGeneration: %v", err)
	}

	if _, err := os.Lstat(filepath.Join(galeDir, "current", "bin", "go")); !os.IsNotExist(err) {
		t.Fatalf("go symlink must not come from .tool-versions, err=%v", err)
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
		Resolver: func(_ context.Context, name string) (*recipe.Recipe, error) {
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
	reg, err := registry.NewWithURL(addr)
	if err != nil {
		t.Fatalf("registry.NewWithURL: %v", err)
	}
	ctx := &cmdContext{
		Resolver: func(_ context.Context, name string) (*recipe.Recipe, error) {
			return want, nil
		},
		Registry: reg,
	}
	_, err = resolveVersionedRecipe(ctx, "atuin", "18.13.6-2")
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

// TestFinalizeInstallWrapsRebuildError guards against bug 0015:
// finalizeInstall returns the raw error from rebuildGeneration without
// wrapping it with "rebuild generation" context. Install and switch
// callers cannot add context they don't have; the wrapper belongs here.
func TestFinalizeInstallWrapsRebuildError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: read-only dirs are still writable")
	}
	tmp := t.TempDir()
	galeDir := filepath.Join(tmp, ".gale")
	storeRoot := filepath.Join(tmp, "pkg")
	configPath := filepath.Join(tmp, "gale.toml")

	// Create a valid package in the store so config+lockfile writes succeed.
	s := store.NewStore(storeRoot)
	pkgDir, err := s.Create("jq", "1.8.1-1")
	if err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(pkgDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "jq"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("[packages]\n  jq = \"1.8.1\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Make the gale directory read-only so generation.Build fails.
	if err := os.MkdirAll(galeDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(galeDir, 0o755) }) //nolint:gosec

	ctx := &cmdContext{
		GaleDir:   galeDir,
		StoreRoot: storeRoot,
		GalePath:  configPath,
	}
	writeProvenance(t, storeRoot, "jq", "1.8.1-1")
	err = ctx.FinalizeInstall("jq", "1.8.1", "1.8.1-1")
	if err == nil {
		t.Fatal("expected FinalizeInstall to return error on rebuild failure")
	}
	if !strings.Contains(err.Error(), "rebuild generation") {
		t.Errorf("FinalizeInstall error %q does not contain 'rebuild generation' context", err.Error())
	}
}

// TestNewRegistryKeepsCachingWithoutHome carries gh#254's contract
// to the command layer. `gale` runs from cron jobs, systemd units,
// launchd agents and `sudo` without -H, none of which export $HOME.
// defaultCacheDir answered that with "", newRegistry stored it, and
// cachedGet reads "" as "no cache configured" — so every recipe
// read in those contexts went to the network uncached, with nothing
// in the output saying so.
func TestNewRegistryKeepsCachingWithoutHome(t *testing.T) {
	t.Setenv("HOME", "")

	reg, err := newRegistry()
	if err != nil {
		t.Fatalf("newRegistry() error = %v; the system temp dir is "+
			"usable, so a missing $HOME must fall back to it", err)
	}
	if reg.CacheDir == "" {
		t.Error("newRegistry() left CacheDir empty without $HOME, " +
			"silently disabling registry caching (gh#254)")
	}
}

// TestNewRegistryFailsWhenNoCacheLocationUsable pins the far end:
// when nothing on the machine can hold a cache, the command layer
// gets a real error instead of a quietly uncached registry.
func TestNewRegistryFailsWhenNoCacheLocationUsable(t *testing.T) {
	breakSystemTemp(t)
	t.Setenv("HOME", "")

	reg, err := newRegistry()
	if err == nil {
		t.Fatalf("newRegistry() = (%+v, nil) with no usable cache "+
			"location; want an error", reg)
	}
}
