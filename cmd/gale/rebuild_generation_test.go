package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/recipe"
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
	if err := rebuildGeneration(galeDir, storeRoot, configPath, nil); err != nil {
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
// Retention is the compiled constant 2 (current + one
// previous), so staging 15 pre-existing gens plus a fresh
// Build (which makes #16) should result in gens 1..14
// removed, gens 15..16 preserved.
func TestRebuildGenerationAutoPrunesOldGens(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
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

	if err := rebuildGeneration(galeDir, storeRoot, configPath, nil); err != nil {
		t.Fatalf("rebuildGeneration: %v", err)
	}

	// Build advanced current to gen/16. Auto-gc keeps the last
	// 2 (gens 15..16), prunes 1..14.
	for i := 1; i <= 14; i++ {
		if _, err := os.Stat(
			filepath.Join(galeDir, "gen", strconv.Itoa(i)),
		); !os.IsNotExist(err) {
			t.Errorf("gen/%d should have been auto-pruned (err=%v)", i, err)
		}
	}
	for i := 15; i <= 16; i++ {
		if _, err := os.Stat(
			filepath.Join(galeDir, "gen", strconv.Itoa(i)),
		); err != nil {
			t.Errorf("gen/%d should be preserved: %v", i, err)
		}
	}
}

// TestRebuildGenerationIgnoresConfigKeep pins that
// [generation] keep in config.toml cannot change or disable
// auto-prune. keep = -1 used to skip prune; keep = 10 used
// to retain a week of gens. Both must now prune to 2.
func TestRebuildGenerationIgnoresConfigKeep(t *testing.T) {
	for _, keep := range []string{"-1", "10"} {
		t.Run("keep="+keep, func(t *testing.T) {
			writeAppKeepConfig(t, keep)
			galeDir, storeRoot, configPath := stageFifteenGenRebuild(t)
			if err := rebuildGeneration(galeDir, storeRoot, configPath, nil); err != nil {
				t.Fatalf("rebuildGeneration: %v", err)
			}
			assertKeepTwoAfterRebuild(t, galeDir, keep)
		})
	}
}

func writeAppKeepConfig(t *testing.T, keep string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".gale"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(home, ".gale", "config.toml"),
		[]byte("[generation]\nkeep = "+keep+"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
}

func stageFifteenGenRebuild(t *testing.T) (galeDir, storeRoot, configPath string) {
	t.Helper()
	galeDir = t.TempDir()
	storeRoot = t.TempDir()
	configPath = filepath.Join(galeDir, "gale.toml")
	if err := os.MkdirAll(storeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
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
	return galeDir, storeRoot, configPath
}

func assertKeepTwoAfterRebuild(t *testing.T, galeDir, keep string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(galeDir, "gen", "14")); !os.IsNotExist(err) {
		t.Errorf("keep = %s must not disable or widen prune; "+
			"gen/14 still exists (err=%v)", keep, err)
	}
	if _, err := os.Stat(filepath.Join(galeDir, "gen", "16")); err != nil {
		t.Errorf("gen/16 (current) must survive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(galeDir, "gen", "15")); err != nil {
		t.Errorf("gen/15 (previous) must survive: %v", err)
	}
}

func TestRebuildGenerationLinksRecipeRevisionOverOrphan(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()
	configPath := filepath.Join(galeDir, "gale.toml")

	for _, ver := range []string{"1.48.0-1", "1.48.0-2"} {
		binDir := filepath.Join(storeRoot, "just", ver, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(binDir, "just"), []byte("#!/bin/sh\n"), 0o755,
		); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.WriteFile(configPath,
		[]byte("[packages]\njust = \"1.48.0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	pinResolve := versionedRecipeResolver(func(name, version string) (*recipe.Recipe, error) {
		if name != "just" || version != "1.48.0" {
			return nil, fmt.Errorf("unexpected pin %s@%s", name, version)
		}
		return &recipe.Recipe{
			Package: recipe.Package{
				Name: "just", Version: "1.48.0", Revision: 1,
			},
		}, nil
	})

	if err := rebuildGeneration(
		galeDir, storeRoot, configPath, pinResolve,
	); err != nil {
		t.Fatalf("rebuildGeneration: %v", err)
	}

	target, err := os.Readlink(
		filepath.Join(galeDir, "gen", "1", "bin", "just"),
	)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	wantFragment := filepath.Join("just", "1.48.0-1", "bin", "just")
	if !strings.Contains(target, wantFragment) {
		t.Errorf("just symlink target = %q, want fragment %q",
			target, wantFragment)
	}
	if strings.Contains(target, "1.48.0-2") {
		t.Errorf("just symlink must not point at orphan 1.48.0-2: %q",
			target)
	}
}

// TestRebuildGenerationActivatesPresentWhenUnrelatedMissing is a
// regression guard for gh#123: `gale update <pkg>` built a new
// version into the store, but the generation never rotated
// because an *unrelated* package declared in gale.toml was
// missing from the store, and the strict rebuild aborted on it.
// `update` and `remove` both rebuild through rebuildGeneration,
// which since #99 (v0.17.0) delegates to the lenient path: the
// present (just-built) package activates and the missing
// unrelated one is skipped with a warning, instead of aborting
// and forcing a full `gale sync`.
//
// This pins that behavior at the rebuildGeneration function the
// CLI actually calls. The finishUpdate* tests stub the rebuild
// func, and TestFinalizeInstallWithMissingOtherPkgInConfig covers
// the install path (finalizeInstall) — neither guards this shared
// function against a regression back to strict generation.Build.
func TestRebuildGenerationActivatesPresentWhenUnrelatedMissing(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()
	configPath := filepath.Join(galeDir, "gale.toml")

	// Stage only the just-built package in the store, using the
	// "-1" revision layout the real store uses.
	binDir := filepath.Join(storeRoot, "gale", "0.19.0-1", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir gale: %v", err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "gale"),
		[]byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write gale: %v", err)
	}

	// Declare the just-built package plus an unrelated one that is
	// NOT staged in the store — the exact #123 scenario.
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n  gale = \"0.19.0\"\n  awscli = \"2.34.19\"\n"),
		0o644); err != nil {
		t.Fatalf("write gale.toml: %v", err)
	}

	// The shared rebuild path must not abort on the missing
	// unrelated package — strict Build would error here.
	if err := rebuildGeneration(galeDir, storeRoot, configPath, nil); err != nil {
		t.Fatalf("rebuildGeneration aborted on a missing unrelated package "+
			"(gh#123 regression): %v", err)
	}

	genBinDir := filepath.Join(galeDir, "gen", "1", "bin")
	entries, err := os.ReadDir(genBinDir)
	if err != nil {
		t.Fatalf("read gen bin: %v", err)
	}
	got := make(map[string]bool, len(entries))
	for _, e := range entries {
		got[e.Name()] = true
	}

	if !got["gale"] {
		t.Errorf("gen/1/bin missing %q: the just-built package did not "+
			"activate, entries=%v", "gale", entries)
	}
	if got["awscli"] {
		t.Errorf("gen/1/bin has %q: a package missing from the store should "+
			"be skipped, not linked", "awscli")
	}
}
