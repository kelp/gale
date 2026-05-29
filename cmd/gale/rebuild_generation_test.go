package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/lockfile"
)

// TestRebuildGenerationOverManyPackagesSymlinksAll is a
// regression test for an observed production failure: a user
// with 44 declared packages ended up with only ~23 binaries in
// current/bin after `just install`. This test exercises the
// path that `just install` actually takes — finalizeInstall →
// rebuildGeneration → reads gale.toml → generation.Build —
// with 30 declared packages, each backed by a matching store
// dir.
//
// If this test fails with fewer than 30 binaries in gen/N/bin,
// the bug is in rebuildGeneration or readConfigPackages. If it
// passes, the bug is somewhere else (e.g., the install pipeline
// is mutating gale.toml between adds and rebuild).
func TestRebuildGenerationOverManyPackagesSymlinksAll(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()
	configPath := filepath.Join(galeDir, "gale.toml")

	// 30 packages, varied names spanning the alphabet — mirror
	// the user's gale.toml shape (44 declared, single binary each).
	names := []string{
		"atuin", "autossh", "bat", "btop", "chezmoi",
		"curl", "difftastic", "direnv", "doggo", "dust",
		"fd", "fish", "fzf", "gale", "gh",
		"git", "glow", "go", "gopls", "jq",
		"just", "lazygit", "mise", "neovim", "pnpm",
		"ripgrep", "starship", "uv", "yq", "zmx",
	}
	const version = "1.0.0"

	// Stage each package in the store at <storeRoot>/<name>/<version>-1/bin/<name>.
	// Use a "-1" revision suffix to match the revision-system
	// layout the user has on disk.
	for _, name := range names {
		binDir := filepath.Join(storeRoot, name, version+"-1", "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", binDir, err)
		}
		exe := filepath.Join(binDir, name)
		if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("write %s: %v", exe, err)
		}
	}

	// Write a gale.toml that declares each package at the
	// bare version (no revision suffix) — matches the
	// real-world gale.toml convention.
	var b strings.Builder
	b.WriteString("[packages]\n")
	for _, name := range names {
		fmt.Fprintf(&b, "  %s = %q\n", name, version)
	}
	if err := os.WriteFile(configPath, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write gale.toml: %v", err)
	}

	// Exercise the EXACT function the install path uses.
	if err := rebuildGeneration(galeDir, storeRoot, configPath); err != nil {
		t.Fatalf("rebuildGeneration: %v", err)
	}

	// Walk gen/1/bin and collect symlink names.
	genBinDir := filepath.Join(galeDir, "gen", "1", "bin")
	entries, err := os.ReadDir(genBinDir)
	if err != nil {
		t.Fatalf("read gen bin: %v", err)
	}
	got := make([]string, 0, len(entries))
	for _, e := range entries {
		got = append(got, e.Name())
	}
	sort.Strings(got)

	want := append([]string(nil), names...)
	sort.Strings(want)

	if len(got) != len(want) {
		t.Errorf("gen/1/bin has %d entries, want %d", len(got), len(want))
	}
	for _, name := range want {
		found := false
		for _, g := range got {
			if g == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("gen/1/bin missing %q (have %d/%d): %v",
				name, len(got), len(want), got)
		}
	}
}

// TestRebuildGenerationAutoPrunesOldGens pins the auto-gc
// behavior: every successful rebuildGeneration call triggers
// generation.PruneOldGenerations with the configured keep
// count. Without this, gens accumulate per-install and chew
// through inodes — the dev-host incident hit ~3M inodes for
// 33 untouched gens before manual gc.
//
// Default retention is config.DefaultGenerationKeep (10), so
// staging 15 pre-existing gens plus a fresh Build (which
// makes #16) should result in gens 1..6 removed, gens 7..16
// preserved.
func TestRebuildGenerationAutoPrunesOldGens(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()
	configPath := filepath.Join(galeDir, "gale.toml")

	if err := os.MkdirAll(storeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	// Single package staged in the store so Build has something
	// real to link. The pre-existing gen dirs are empty stubs —
	// auto-gc only cares about their numeric names.
	binDir := filepath.Join(storeRoot, "jq", "1.0.0", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "jq"),
		[]byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n  jq = \"1.0.0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Stage gens 1..15 as stub dirs. current → gen/15 so the
	// next Build produces gen/16.
	for i := 1; i <= 15; i++ {
		if err := os.MkdirAll(
			filepath.Join(galeDir, "gen", strconv.Itoa(i), "bin"),
			0o755,
		); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join("gen", "15"),
		filepath.Join(galeDir, "current")); err != nil {
		t.Fatal(err)
	}

	if err := rebuildGeneration(galeDir, storeRoot, configPath); err != nil {
		t.Fatalf("rebuildGeneration: %v", err)
	}

	// Build advanced current to gen/16. Auto-gc keeps the last
	// 10 (gens 7..16), prunes 1..6.
	for i := 1; i <= 6; i++ {
		if _, err := os.Stat(
			filepath.Join(galeDir, "gen", strconv.Itoa(i)),
		); !os.IsNotExist(err) {
			t.Errorf("gen/%d should have been auto-pruned (err=%v)", i, err)
		}
	}
	for i := 7; i <= 16; i++ {
		if _, err := os.Stat(
			filepath.Join(galeDir, "gen", strconv.Itoa(i)),
		); err != nil {
			t.Errorf("gen/%d should be preserved: %v", i, err)
		}
	}
}

// TestFinalizeInstallRotatesGenOnRevisionBump is a regression
// test for issue #23 (install --recipe leaves the new revision
// off PATH). The user-facing contract of `gale install` is
// "this package is now on PATH" — if a bumped revision lands
// in the store and the lockfile but the current generation
// still points at the old revision, the install silently lied.
//
// Sequence:
//  1. Stage gh@2.92.0-2 in the store and build gen/1 with it.
//     current → gen/1, gen/1/bin/gh → pkg/gh/2.92.0-2/bin/gh.
//  2. A `gale install --recipe gh.toml` for revision 3 stages
//     pkg/gh/2.92.0-3/ (we simulate that).
//  3. Call finalizeInstall with the exact arguments
//     installFromRecipeFile uses: name="gh", configVersion
//     ="2.92.0" (bare), lockVersion="2.92.0-3", sha="deadbeef".
//
// Expected: no error; current advances to gen/2; the new
// symlink resolves under 2.92.0-3.
//
// The original issue was a v0.12.3 gale binary running inside
// the gale repo (its resolver appended "-1" unconditionally and
// silently skipped ENOENT — the root cause of the gen/308
// incident, fixed in 773c051). This test pins the modern
// resolver's contract so a future regression of the same
// shape gets caught immediately.
func TestFinalizeInstallRotatesGenOnRevisionBump(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()
	configPath := filepath.Join(galeDir, "gale.toml")
	lockPath := filepath.Join(galeDir, "gale.lock")

	mkRev := func(rev string) {
		t.Helper()
		binDir := filepath.Join(storeRoot, "gh", rev, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", binDir, err)
		}
		if err := os.WriteFile(filepath.Join(binDir, "gh"),
			[]byte("#!/bin/sh\n# "+rev+"\n"), 0o755); err != nil {
			t.Fatalf("write %s: %v", binDir, err)
		}
	}

	mkRev("2.92.0-2")
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n  gh = \"2.92.0\"\n"), 0o644); err != nil {
		t.Fatalf("write gale.toml: %v", err)
	}
	if err := lockfile.Write(lockPath, &lockfile.LockFile{
		Packages: map[string]lockfile.LockedPackage{
			"gh": {Version: "2.92.0-2", SHA256: "old"},
		},
	}); err != nil {
		t.Fatalf("write initial lockfile: %v", err)
	}
	if err := rebuildGeneration(galeDir, storeRoot, configPath); err != nil {
		t.Fatalf("initial rebuildGeneration: %v", err)
	}

	gen1Link := filepath.Join(galeDir, "gen", "1", "bin", "gh")
	if t1, err := os.Readlink(gen1Link); err != nil {
		t.Fatalf("gen/1 sanity readlink: %v", err)
	} else if !strings.HasSuffix(t1, filepath.Join("gh", "2.92.0-2", "bin", "gh")) {
		t.Fatalf("gen/1/bin/gh → %s, want suffix gh/2.92.0-2/bin/gh", t1)
	}

	mkRev("2.92.0-3")
	if err := finalizeInstall(galeDir, storeRoot, configPath, "",
		"gh", "2.92.0", "2.92.0-3", "deadbeef"); err != nil {
		t.Fatalf("finalizeInstall on revision bump: %v", err)
	}

	current, err := os.Readlink(filepath.Join(galeDir, "current"))
	if err != nil {
		t.Fatalf("read current after finalize: %v", err)
	}
	if filepath.Base(current) != "2" {
		t.Errorf("current → %s, want gen/2 (rotation failed)", current)
	}

	target, err := os.Readlink(filepath.Join(galeDir, "gen", "2", "bin", "gh"))
	if err != nil {
		t.Fatalf("readlink gen/2/bin/gh: %v", err)
	}
	wantSuffix := filepath.Join("gh", "2.92.0-3", "bin", "gh")
	if !strings.HasSuffix(target, wantSuffix) {
		t.Errorf("gen/2/bin/gh → %s, want suffix %s", target, wantSuffix)
	}
}

// TestFinalizeInstallWithMissingOtherPkgInConfig verifies that
// finalizeInstall is lenient: installing one package succeeds
// and lands on PATH even when gale.toml lists another package
// whose store dir is absent. The uninstalled package is skipped
// silently — it is installed later via `gale sync`. gale.toml
// records intent; a single `gale install` only installs the
// requested package.
func TestFinalizeInstallWithMissingOtherPkgInConfig(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()
	configPath := filepath.Join(galeDir, "gale.toml")
	lockPath := filepath.Join(galeDir, "gale.lock")

	binDir := filepath.Join(storeRoot, "gh", "2.92.0-2", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir gh: %v", err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "gh"),
		[]byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write gh: %v", err)
	}

	if err := os.WriteFile(configPath,
		[]byte("[packages]\n  gh = \"2.92.0\"\n  missing = \"1.0.0\"\n"),
		0o644); err != nil {
		t.Fatalf("write gale.toml: %v", err)
	}
	if err := lockfile.Write(lockPath, &lockfile.LockFile{
		Packages: map[string]lockfile.LockedPackage{
			"gh": {Version: "2.92.0-2", SHA256: "old"},
		},
	}); err != nil {
		t.Fatalf("write lockfile: %v", err)
	}

	newBin := filepath.Join(storeRoot, "gh", "2.92.0-3", "bin")
	if err := os.MkdirAll(newBin, 0o755); err != nil {
		t.Fatalf("mkdir gh-3: %v", err)
	}
	if err := os.WriteFile(filepath.Join(newBin, "gh"),
		[]byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write gh-3: %v", err)
	}

	err := finalizeInstall(galeDir, storeRoot, configPath, "",
		"gh", "2.92.0", "2.92.0-3", "deadbeef")
	if err != nil {
		t.Fatalf("finalizeInstall should be lenient (skip uninstalled config pkgs) and succeed, got: %v", err)
	}

	active, err := generation.CurrentVersions(galeDir, storeRoot)
	if err != nil {
		t.Fatalf("CurrentVersions: %v", err)
	}
	if v, ok := active["gh"]; !ok || v != "2.92.0-3" {
		t.Errorf("active[gh] = %q (present=%v), want 2.92.0-3", v, ok)
	}
	if _, ok := active["missing"]; ok {
		t.Errorf("active[missing] = %q, want absent (uninstalled pkg should be skipped)", active["missing"])
	}

	if _, statErr := os.Lstat(filepath.Join(galeDir, "current")); statErr != nil {
		t.Errorf("current symlink should exist after lenient rebuild: %v", statErr)
	}
}

// TestFinalizeInstallPreservesAllDeclaredPackages exercises the
// EXACT path `just install` takes: write a gale.toml with N
// pre-existing packages, stage their store dirs, then call
// finalizeInstall (which adds one more package + lockfile entry
// + rebuilds the generation). Assert the resulting gen has all
// N+1 packages' binaries.
//
// This is the closest unit test to the user-reported regression
// where 'just install' produced a gen with only 6/44 packages'
// binaries present. If this test fails, the regression is in
// finalizeInstall's gale.toml mutation or in the rebuild chain.
func TestFinalizeInstallPreservesAllDeclaredPackages(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()
	configPath := filepath.Join(galeDir, "gale.toml")
	lockPath := filepath.Join(galeDir, "gale.lock")

	// 44 packages, names spanning the alphabet — mirror the
	// user's gale.toml shape (44 declared, single binary each).
	preexisting := []string{
		"1password-cli", "atuin", "autossh", "bat", "btop",
		"chezmoi", "curl", "difftastic", "direnv", "doctl",
		"doggo", "dust", "fd", "fish", "fzf",
		"gh", "git", "git-delta", "glib", "glow",
		"go", "gopls", "gping", "hyperfine", "jq",
		"just", "lazygit", "lua", "mise", "neovim",
		"nodejs", "pkgconf", "pnpm", "procs", "ruby",
		"rustup", "scc", "sqlite", "starship", "tealdeer",
		"tree-sitter", "trippy", "uv", "vibeutils",
	}
	const version = "1.0.0"

	for _, name := range preexisting {
		binDir := filepath.Join(storeRoot, name, version+"-1", "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", binDir, err)
		}
		exe := filepath.Join(binDir, name)
		if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("write %s: %v", exe, err)
		}
	}

	// Write gale.toml with all 44 pre-existing packages.
	var b strings.Builder
	b.WriteString("[packages]\n")
	for _, name := range preexisting {
		fmt.Fprintf(&b, "  %s = %q\n", name, version)
	}
	if err := os.WriteFile(configPath, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write gale.toml: %v", err)
	}

	// Initialise empty lockfile so updateLockfile doesn't trip.
	if err := lockfile.Write(lockPath, &lockfile.LockFile{
		Packages: map[string]lockfile.LockedPackage{},
	}); err != nil {
		t.Fatalf("write lockfile: %v", err)
	}

	// Stage the new package being installed (e.g. gale).
	newPkg := "gale"
	const newVersion = "0.16.2"
	binDir := filepath.Join(storeRoot, newPkg, newVersion+"-1", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir new pkg: %v", err)
	}
	exe := filepath.Join(binDir, newPkg)
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write new pkg exe: %v", err)
	}

	// Run finalizeInstall — the EXACT function `just install`
	// calls after building gale from source.
	if err := finalizeInstall(galeDir, storeRoot, configPath, "",
		newPkg, newVersion, newVersion+"-1", "deadbeef"); err != nil {
		t.Fatalf("finalizeInstall: %v", err)
	}

	// Walk gen/1/bin and assert all 44 pre-existing + 1 new = 45 binaries.
	genBinDir := filepath.Join(galeDir, "gen", "1", "bin")
	entries, err := os.ReadDir(genBinDir)
	if err != nil {
		t.Fatalf("read gen bin: %v", err)
	}
	got := make([]string, 0, len(entries))
	for _, e := range entries {
		got = append(got, e.Name())
	}
	sort.Strings(got)

	want := append([]string(nil), preexisting...)
	want = append(want, newPkg)
	sort.Strings(want)

	if len(got) != len(want) {
		t.Errorf("gen/1/bin has %d entries, want %d (%d preexisting + 1 new)",
			len(got), len(want), len(preexisting))
	}
	for _, name := range want {
		found := false
		for _, g := range got {
			if g == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("gen/1/bin missing %q (have %d/%d): %v",
				name, len(got), len(want), got)
		}
	}
}
