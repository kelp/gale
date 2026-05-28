package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

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
