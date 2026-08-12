package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/projects"
)

// addStoreBin adds extra executables to an existing store dir.
func addStoreBin(t *testing.T, pkgDir string, binaries ...string) {
	t.Helper()
	for _, binary := range binaries {
		if err := os.WriteFile(
			filepath.Join(pkgDir, "bin", binary), []byte("#!/bin/sh\n"), 0o755,
		); err != nil {
			t.Fatal(err)
		}
	}
}

// TestRebuildGenerationFailsOnBinCollisionBeforeCurrentMoves pins
// gh#190 at the pipeline layer: two declared packages shipping the
// same bin/ basename must refuse the rebuild, naming both providers,
// while the previous generation stays active.
//
// Pre-fix, populateGeneration sorted package names and let the first
// one win every collision, silently. The user got a binary on PATH
// chosen by sort order with nothing said about the shadowed provider.
//
// Every mutating command reaches generation.Build through
// rebuildGeneration, so driving that helper covers the surface.
func TestRebuildGenerationFailsOnBinCollisionBeforeCurrentMoves(t *testing.T) {
	galeDir, storeRoot := setupGCHome(t)
	configPath := filepath.Join(galeDir, "gale.toml")

	alphaDir := mkStorePkg(t, storeRoot, "alpha", "1.0")
	betaDir := mkStorePkg(t, storeRoot, "beta", "1.0")
	addStoreBin(t, alphaDir, "foo")
	addStoreBin(t, betaDir, "foo")

	// Seed an active generation from the one package that cannot
	// collide, so the refusal below has something to preserve.
	writeGlobalConfig(t, galeDir, "[packages]\nalpha = \"1.0\"\n")
	if err := rebuildGeneration(galeDir, storeRoot, configPath, nil); err != nil {
		t.Fatalf("seed rebuild: %v", err)
	}
	if cur, err := generation.Current(galeDir); err != nil || cur != 1 {
		t.Fatalf("setup: current = %d (err=%v), want 1", cur, err)
	}

	writeGlobalConfig(t, galeDir,
		"[packages]\nalpha = \"1.0\"\nbeta = \"1.0\"\n")

	err := rebuildGeneration(galeDir, storeRoot, configPath, nil)
	if err == nil {
		t.Fatal("rebuildGeneration succeeded, want a bin collision error")
	}

	var collErr *generation.BinCollisionError
	if !errors.As(err, &collErr) {
		t.Fatalf("error type = %T (%v), want *generation.BinCollisionError",
			err, err)
	}
	for _, want := range []string{"foo", "alpha", "beta"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q — the user cannot fix a "+
				"collision whose providers are not reported", err, want)
		}
	}

	cur, err := generation.Current(galeDir)
	if err != nil {
		t.Fatalf("read current: %v", err)
	}
	if cur != 1 {
		t.Errorf("current generation = %d, want 1 — a refused rebuild "+
			"must leave the previous generation active", cur)
	}
	if _, err := os.Stat(
		filepath.Join(galeDir, "gen", "2"),
	); !os.IsNotExist(err) {
		t.Errorf("gen/2 exists (err=%v) — the refusal must precede the "+
			"swap and leave nothing behind", err)
	}
}

// TestRebuildGenerationHonorsBinOverride covers gh#190's escape
// hatch: [bin] names the winning package for a basename, and every
// other provider's entry is left out of the generation.
func TestRebuildGenerationHonorsBinOverride(t *testing.T) {
	galeDir, storeRoot := setupGCHome(t)
	configPath := filepath.Join(galeDir, "gale.toml")

	alphaDir := mkStorePkg(t, storeRoot, "alpha", "1.0")
	betaDir := mkStorePkg(t, storeRoot, "beta", "1.0")
	addStoreBin(t, alphaDir, "foo")
	addStoreBin(t, betaDir, "foo")

	writeGlobalConfig(t, galeDir,
		"[packages]\nalpha = \"1.0\"\nbeta = \"1.0\"\n\n"+
			"[bin]\nfoo = \"beta\"\n")

	if err := rebuildGeneration(galeDir, storeRoot, configPath, nil); err != nil {
		t.Fatalf("rebuildGeneration with [bin] override: %v", err)
	}

	got := evalPath(t, filepath.Join(galeDir, "current", "bin", "foo"))
	want := evalPath(t, filepath.Join(betaDir, "bin", "foo"))
	if got != want {
		t.Errorf("bin/foo resolves to %q, want %q — [bin] names the "+
			"winner, not sort order", got, want)
	}

	// The loser keeps its own uncontested binaries.
	if _, err := os.Stat(
		filepath.Join(galeDir, "current", "bin", "alpha"),
	); err != nil {
		t.Errorf("alpha's own binary fell off PATH: %v", err)
	}
}

// TestRebuildGenerationRejectsUnknownBinOverride keeps the override
// honest: a winner that is not a declared package is a typo, and
// silently ignoring it would restore the silent-shadowing bug.
func TestRebuildGenerationRejectsUnknownBinOverride(t *testing.T) {
	galeDir, storeRoot := setupGCHome(t)
	configPath := filepath.Join(galeDir, "gale.toml")

	alphaDir := mkStorePkg(t, storeRoot, "alpha", "1.0")
	betaDir := mkStorePkg(t, storeRoot, "beta", "1.0")
	addStoreBin(t, alphaDir, "foo")
	addStoreBin(t, betaDir, "foo")

	writeGlobalConfig(t, galeDir,
		"[packages]\nalpha = \"1.0\"\nbeta = \"1.0\"\n\n"+
			"[bin]\nfoo = \"gamma\"\n")

	err := rebuildGeneration(galeDir, storeRoot, configPath, nil)
	if err == nil {
		t.Fatal("rebuildGeneration accepted an undeclared [bin] winner")
	}
	if !strings.Contains(err.Error(), "gamma") {
		t.Errorf("error %q does not name the undeclared winner", err)
	}
}

// TestRebuildGenerationDetectsCollisionFromCarriedForwardVersion
// covers the interaction with carryForwardMissingVersions: it
// rewrites the package map before populateGeneration, so a carried-
// forward version can introduce a collision the declared set does
// not have. Detection must see the map the generation is built from.
func TestRebuildGenerationDetectsCollisionFromCarriedForwardVersion(t *testing.T) {
	galeDir, storeRoot := setupGCHome(t)
	configPath := filepath.Join(galeDir, "gale.toml")

	mkStorePkg(t, storeRoot, "alpha", "1.0")
	alpha2 := mkStorePkg(t, storeRoot, "alpha", "2.0")
	beta1 := mkStorePkg(t, storeRoot, "beta", "1.0")
	addStoreBin(t, alpha2, "foo")
	addStoreBin(t, beta1, "foo")

	// gen/1: alpha@1.0 ships no foo, so nothing collides.
	writeGlobalConfig(t, galeDir,
		"[packages]\nalpha = \"1.0\"\nbeta = \"1.0\"\n")
	if err := rebuildGeneration(galeDir, storeRoot, configPath, nil); err != nil {
		t.Fatalf("seed rebuild: %v", err)
	}

	// beta@2.0 was never installed, so the rebuild carries beta@1.0
	// forward — and that version does ship foo, which alpha@2.0 now
	// ships too.
	writeGlobalConfig(t, galeDir,
		"[packages]\nalpha = \"2.0\"\nbeta = \"2.0\"\n")

	err := rebuildGeneration(galeDir, storeRoot, configPath, nil)
	var collErr *generation.BinCollisionError
	if !errors.As(err, &collErr) {
		t.Fatalf("error = %v (%T), want *generation.BinCollisionError — a "+
			"carried-forward version can introduce a collision", err, err)
	}
	if !strings.Contains(err.Error(), "foo") {
		t.Errorf("error %q does not name the colliding binary", err)
	}
}

// evalPath resolves a path through symlinks for comparison. macOS
// /var is a symlink to /private/var, so both sides need it.
func evalPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolve %s: %v", path, err)
	}
	return resolved
}

// TestRegenerateScopeHonorsBinOverride covers migrate's interaction
// with gh#190. A generation built before the fix can hold a shadowed
// executable, and migrate rebuilds a scope from that active package
// set — so it must read the scope's [bin] table. Without it the
// override would fix every other command and leave migrate stuck on
// a collision the user has already resolved.
func TestRegenerateScopeHonorsBinOverride(t *testing.T) {
	galeDir, storeRoot := setupGCHome(t)

	alphaDir := mkStorePkg(t, storeRoot, "alpha", "1.0")
	betaDir := mkStorePkg(t, storeRoot, "beta", "1.0")
	addStoreBin(t, alphaDir, "foo")
	addStoreBin(t, betaDir, "foo")

	pkgs := map[string]string{"alpha": "1.0", "beta": "1.0"}
	if err := generation.BuildWithOptions(
		pkgs, galeDir, storeRoot,
		generation.Options{BinOverrides: map[string]string{"foo": "beta"}},
	); err != nil {
		t.Fatalf("seed generation: %v", err)
	}

	scope := projects.Scope{Label: "the global scope", GaleDir: galeDir}

	// No manifest yet: the collision is in the active set, so the
	// rebuild refuses it.
	err := regenerateScope(scope, storeRoot, discardOutput())
	var collErr *generation.BinCollisionError
	if !errors.As(err, &collErr) {
		t.Fatalf("error = %v (%T), want *generation.BinCollisionError",
			err, err)
	}

	writeGlobalConfig(t, galeDir,
		"[packages]\nalpha = \"1.0\"\nbeta = \"1.0\"\n\n"+
			"[bin]\nfoo = \"beta\"\n")

	if err := regenerateScope(scope, storeRoot, discardOutput()); err != nil {
		t.Fatalf("regenerateScope with [bin] override: %v", err)
	}
	if got := linkTarget(t, galeDir, "foo"); !strings.Contains(
		got, filepath.Join("beta", "1.0"),
	) {
		t.Errorf("bin/foo -> %s, want beta's copy", got)
	}
}

// TestRemovePrunesBinOverrideNamingRemovedPackage closes the trap
// this PR would otherwise ship: `gale remove beta` while [bin] names
// beta as a winner left a manifest that no longer loads, so every
// command in the scope failed until the user hand-edited the file.
// The user did nothing wrong — they added the entry gale told them to
// add — and the state is worse than the collision it prevents, which
// at least left a working PATH.
func TestRemovePrunesBinOverrideNamingRemovedPackage(t *testing.T) {
	galeDir, storeRoot := setupGCHome(t)
	configPath := filepath.Join(galeDir, "gale.toml")

	alphaDir := mkStorePkg(t, storeRoot, "alpha", "1.0")
	betaDir := mkStorePkg(t, storeRoot, "beta", "1.0")
	addStoreBin(t, alphaDir, "foo")
	addStoreBin(t, betaDir, "foo")

	writeGlobalConfig(t, galeDir,
		"[packages]\nalpha = \"1.0\"\nbeta = \"1.0\"\n\n"+
			"[bin]\nfoo = \"beta\"\n")
	if err := rebuildGeneration(galeDir, storeRoot, configPath, nil); err != nil {
		t.Fatalf("seed rebuild: %v", err)
	}

	removeGlobal = true
	t.Cleanup(func() { removeGlobal = false })
	if err := removeCmd.RunE(removeCmd, []string{"beta"}); err != nil {
		t.Fatalf("remove beta: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.ParseGaleConfig(string(data))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if winner, has := cfg.Bin["foo"]; has {
		t.Errorf("[bin] foo = %q survived the removal of its winner:\n%s",
			winner, data)
	}

	// The manifest must load — that is the whole point.
	if _, err := loadEffectiveConfig(configPath); err != nil {
		t.Errorf("config no longer loads after remove: %v", err)
	}
	if err := rebuildGeneration(galeDir, storeRoot, configPath, nil); err != nil {
		t.Errorf("rebuild after remove: %v", err)
	}
}

// TestRemoveKeepsBinOverrideStillDeclaredElsewhere scopes the prune.
// A host-targeted removal that leaves the winner declared in another
// section must not delete the entry: the override still names a
// package this manifest declares.
func TestRemoveKeepsBinOverrideStillDeclaredElsewhere(t *testing.T) {
	galeDir, storeRoot := setupGCHome(t)
	t.Setenv("GALE_HOST", "testhost")
	configPath := filepath.Join(galeDir, "gale.toml")

	mkStorePkg(t, storeRoot, "alpha", "1.0")
	mkStorePkg(t, storeRoot, "beta", "1.0")

	writeGlobalConfig(t, galeDir,
		"[packages]\nalpha = \"1.0\"\nbeta = \"1.0\"\n\n"+
			"[bin]\nfoo = \"beta\"\n\n"+
			"[hosts.testhost.packages]\nbeta = \"1.0\"\n")

	removeGlobal = true
	removeHost = "testhost"
	t.Cleanup(func() { removeGlobal = false; removeHost = "" })
	if err := removeCmd.RunE(removeCmd, []string{"beta"}); err != nil {
		t.Fatalf("remove beta from the host section: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.ParseGaleConfig(string(data))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if cfg.Bin["foo"] != "beta" {
		t.Errorf("[bin] foo = %q, want beta — shared [packages] still "+
			"declares it:\n%s", cfg.Bin["foo"], data)
	}
}

// TestRemoveKeepsBinOverrideWhenLoserGoes records the decision on the
// other half of the question: removing the package that LOST a
// basename leaves the entry alone. It still names a declared package,
// so the manifest stays valid, and deleting it would quietly re-open
// the collision for anyone who reinstalls the loser later — the entry
// says which package the user chose, and that answer outlives the
// other package's presence.
func TestRemoveKeepsBinOverrideWhenLoserGoes(t *testing.T) {
	galeDir, storeRoot := setupGCHome(t)
	configPath := filepath.Join(galeDir, "gale.toml")

	mkStorePkg(t, storeRoot, "alpha", "1.0")
	mkStorePkg(t, storeRoot, "beta", "1.0")
	writeGlobalConfig(t, galeDir,
		"[packages]\nalpha = \"1.0\"\nbeta = \"1.0\"\n\n"+
			"[bin]\nfoo = \"beta\"\n")

	removeGlobal = true
	t.Cleanup(func() { removeGlobal = false })
	if err := removeCmd.RunE(removeCmd, []string{"alpha"}); err != nil {
		t.Fatalf("remove alpha: %v", err)
	}

	cfg, err := loadEffectiveConfig(configPath)
	if err != nil {
		t.Fatalf("config no longer loads: %v", err)
	}
	if cfg.Bin["foo"] != "beta" {
		t.Errorf("[bin] foo = %q, want beta — the winner is still declared",
			cfg.Bin["foo"])
	}
}

// TestPinPreservesBinOverrides guards the other way a [bin] entry can
// vanish: PinPackage rewrites gale.toml through a struct round-trip,
// which drops any section the struct does not carry. [bin] is a field
// now, so it survives — this test is what keeps it one.
func TestPinPreservesBinOverrides(t *testing.T) {
	galeDir, storeRoot := setupGCHome(t)
	configPath := filepath.Join(galeDir, "gale.toml")

	mkStorePkg(t, storeRoot, "alpha", "1.0")
	mkStorePkg(t, storeRoot, "beta", "1.0")
	writeGlobalConfig(t, galeDir,
		"[packages]\nalpha = \"1.0\"\nbeta = \"1.0\"\n\n"+
			"[bin]\nfoo = \"beta\"\n")

	if err := config.PinPackage(configPath, "", "beta"); err != nil {
		t.Fatalf("PinPackage: %v", err)
	}

	cfg, err := loadEffectiveConfig(configPath)
	if err != nil {
		t.Fatalf("config no longer loads: %v", err)
	}
	if cfg.Bin["foo"] != "beta" {
		t.Errorf("[bin] foo = %q after pin, want beta", cfg.Bin["foo"])
	}
}
