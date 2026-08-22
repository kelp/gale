package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kelp/gale/internal/generation"
)

// gh#219: a man page or a root-level file two packages both ship is
// resolved by sort order. The rebuild keeps accepting it — see
// TestRebuildGenerationAcceptsManPageCollision below. Doctor no
// longer reports shadowed files.

// addStoreFile writes one file at a gen-relative path inside an
// existing store dir, making its parents.
func addStoreFile(t *testing.T, pkgDir, rel string) {
	t.Helper()
	path := filepath.Join(pkgDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// issue219Home lays out a global scope whose two declared packages
// both ship man/man1/foo.1, and returns (galeDir, storeRoot).
func issue219Home(t *testing.T) (galeDir, storeRoot string) {
	t.Helper()
	galeDir, storeRoot = setupGCHome(t)
	for _, name := range []string{"alpha", "beta"} {
		addStoreFile(t,
			mkStorePkg(t, storeRoot, name, "1.0"), "man/man1/foo.1")
	}
	writeGlobalConfig(t, galeDir,
		"[packages]\nalpha = \"1.0\"\nbeta = \"1.0\"\n")
	return galeDir, storeRoot
}

// TestRebuildGenerationAcceptsManPageCollision guards against the
// patch this change invites: reusing bin/'s rule for man/. A duplicate
// man page must rebuild, and current must advance. bin/ refuses
// because it decides what runs; man/ shows the wrong docs.
func TestRebuildGenerationAcceptsManPageCollision(t *testing.T) {
	galeDir, storeRoot := issue219Home(t)
	configPath := filepath.Join(galeDir, "gale.toml")

	if err := rebuildGeneration(
		galeDir, storeRoot, configPath, nil,
	); err != nil {
		t.Fatalf("rebuildGeneration with a duplicate man page: %v — only "+
			"bin/ collisions refuse a rebuild", err)
	}
	cur, err := generation.Current(galeDir)
	if err != nil {
		t.Fatalf("read current: %v", err)
	}
	if cur != 1 {
		t.Errorf("current generation = %d, want 1", cur)
	}
}

// TestRebuildGenerationLeftoverHostBinDoesNotSettle: leftover
// [hosts.<this-host>.bin] does not settle a collision. The
// generation refuses; there is no per-host winner.
func TestRebuildGenerationLeftoverHostBinDoesNotSettle(t *testing.T) {
	galeDir, storeRoot := setupGCHome(t)
	t.Setenv("GALE_HOST", "testhost")
	configPath := filepath.Join(galeDir, "gale.toml")

	alphaDir := mkStorePkg(t, storeRoot, "alpha", "1.0")
	betaDir := mkStorePkg(t, storeRoot, "beta", "1.0")
	addStoreBin(t, alphaDir, "foo")
	addStoreBin(t, betaDir, "foo")

	writeGlobalConfig(t, galeDir,
		"[packages]\nalpha = \"1.0\"\nbeta = \"1.0\"\n\n"+
			"[bin]\nfoo = \"alpha\"\n\n"+
			"[hosts.testhost.bin]\nfoo = \"beta\"\n")

	assertBinCollision(t, rebuildGeneration(galeDir, storeRoot, configPath, nil))
}

// TestLeftoverHostBinStillLoads: leftover [hosts.<this-host>.bin]
// naming a host-only package still loads. Host [packages]
// stay; the leftover table does not fail the parse.
func TestLeftoverHostBinStillLoads(t *testing.T) {
	galeDir, _ := setupGCHome(t)
	t.Setenv("GALE_HOST", "testhost")
	configPath := filepath.Join(galeDir, "gale.toml")

	writeGlobalConfig(t, galeDir,
		"[packages]\nalpha = \"1.0\"\n\n"+
			"[hosts.testhost.packages]\nbeta = \"1.0\"\n\n"+
			"[hosts.testhost.bin]\nfoo = \"beta\"\n")

	cfg, err := loadEffectiveConfig(configPath)
	if err != nil {
		t.Fatalf("leftover host [bin] must still load: %v", err)
	}
	if got := cfg.Packages["beta"]; got != "1.0" {
		t.Errorf("Packages[beta] = %q, want 1.0 — host packages stay", got)
	}
}
