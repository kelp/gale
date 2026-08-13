package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/store"
)

// gh#183: the store is documented as immutable — "once installed, a
// store entry is never modified" (docs/dev/design.md) — but a local
// `--path` install identified its artifact by `git describe` alone.
// Two builds of two different working trees therefore shared one
// store identity, and the second build renamed its output over the
// first at the same pathname. Every generation that linked that
// pathname silently started resolving to bytes it was never built
// with.
//
// These tests pin the two halves of the acceptance criteria that a
// content-derived identity buys: distinct content gets distinct
// store directories, and an earlier generation keeps resolving to
// the bytes it was built with.

// localFixture is one `gale install --path` scenario: where the
// source is, which recipe describes it, and the store and gale home
// the install writes into.
type localFixture struct {
	srcDir     string
	recipePath string
	storeRoot  string
	galeDir    string
	ctx        *cmdContext
}

// localSourceFixture builds the smallest thing that exercises the
// `--path` install end to end: a tagged git repo holding one tracked
// "marker" file, a sibling gale-recipes recipe that copies the
// marker into $PREFIX/bin, and a gale home with an empty gale.toml.
//
// No network: the recipe declares no [source] url, so BuildLocal
// builds the checked-out tree directly.
func localSourceFixture(t *testing.T) localFixture {
	t.Helper()
	tmp := t.TempDir()

	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMarker(t, srcDir, "one")
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
		{"config", "commit.gpgsign", "false"},
		{"add", "marker"},
		{"commit", "-m", "init"},
		{"tag", "v1.0.0"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = srcDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, string(out), err)
		}
	}

	recipesDir := filepath.Join(tmp, "gale-recipes", "recipes", "t")
	if err := os.MkdirAll(recipesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	recipePath := filepath.Join(recipesDir, "testpkg.toml")
	recipeTOML := strings.Join([]string{
		`[package]`,
		`name = "testpkg"`,
		`version = "1.0.0"`,
		``,
		`[build]`,
		`steps = [`,
		`  "mkdir -p $PREFIX/bin",`,
		`  "cp marker $PREFIX/bin/testpkg",`,
		`  "chmod +x $PREFIX/bin/testpkg",`,
		`]`,
	}, "\n")
	if err := os.WriteFile(recipePath, []byte(recipeTOML), 0o644); err != nil {
		t.Fatal(err)
	}

	storeRoot := filepath.Join(tmp, "store")
	galeDir := filepath.Join(tmp, "gale-home")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(galeDir, "gale.toml")
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	return localFixture{
		srcDir:     srcDir,
		recipePath: recipePath,
		storeRoot:  storeRoot,
		galeDir:    galeDir,
		ctx: &cmdContext{
			GalePath:  configPath,
			GaleDir:   galeDir,
			StoreRoot: storeRoot,
			Installer: &installer.Installer{
				Store: store.NewStore(storeRoot),
			},
		},
	}
}

// writeMarker rewrites the tracked file whose content the built
// binary is a copy of, so "the source tree changed" is one line.
func writeMarker(t *testing.T, srcDir, content string) {
	t.Helper()
	if err := os.WriteFile(
		filepath.Join(srcDir, "marker"), []byte(content), 0o644,
	); err != nil {
		t.Fatal(err)
	}
}

// storeVersions lists the committed store directories for one
// package, skipping the dot-prefixed staging siblings and the
// per-package "<version>.lock" files an install leaves under the
// same parent.
func storeVersions(t *testing.T, storeRoot, name string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(storeRoot, name))
	if err != nil {
		t.Fatalf("read store dir for %s: %v", name, err)
	}
	var versions []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		versions = append(versions, e.Name())
	}
	return versions
}

// TestInstallLocalSourceContentGetsDistinctStoreDirs is gh#183's
// first acceptance criterion: two successive `gale install --path`
// builds from different source content produce distinct store
// directories.
//
// Before the fix both builds resolve to "1.0.0" — `git describe`
// says nothing about the working tree — so the second build renames
// its output over the first and the store holds one directory.
func TestInstallLocalSourceContentGetsDistinctStoreDirs(t *testing.T) {
	f := localSourceFixture(t)
	out := output.New(os.Stderr, false)

	if err := installFromLocalSource(
		f.ctx, "testpkg", f.recipePath, f.srcDir, out,
	); err != nil {
		t.Fatalf("first installFromLocalSource: %v", err)
	}

	writeMarker(t, f.srcDir, "two")

	if err := installFromLocalSource(
		f.ctx, "testpkg", f.recipePath, f.srcDir, out,
	); err != nil {
		t.Fatalf("second installFromLocalSource: %v", err)
	}

	got := storeVersions(t, f.storeRoot, "testpkg")
	if len(got) != 2 {
		t.Fatalf("store holds %d testpkg directories (%v), want 2: "+
			"a build of different source content must not overwrite "+
			"the identity an earlier build committed", len(got), got)
	}
}

// TestInstallLocalKeepsEarlierGenerationBytes is gh#183's second
// acceptance criterion: after any reinstall, previously built
// generations still resolve to the bytes they were built with.
//
// This is the criterion with teeth. `gale rollback` selects a
// generation by number and expects the environment that generation
// described; if a later build has rewritten the pathname its
// symlinks point at, the rollback silently executes bytes from a
// tree the user never asked for.
func TestInstallLocalKeepsEarlierGenerationBytes(t *testing.T) {
	f := localSourceFixture(t)
	out := output.New(os.Stderr, false)

	if err := installFromLocalSource(
		f.ctx, "testpkg", f.recipePath, f.srcDir, out,
	); err != nil {
		t.Fatalf("first installFromLocalSource: %v", err)
	}

	genOne := filepath.Join(f.galeDir, "gen", "1", "bin", "testpkg")
	before, err := os.ReadFile(genOne)
	if err != nil {
		t.Fatalf("read %s after the first install: %v", genOne, err)
	}
	if string(before) != "one" {
		t.Fatalf("gen/1 holds %q, want %q", before, "one")
	}

	writeMarker(t, f.srcDir, "two")

	if err := installFromLocalSource(
		f.ctx, "testpkg", f.recipePath, f.srcDir, out,
	); err != nil {
		t.Fatalf("second installFromLocalSource: %v", err)
	}

	after, err := os.ReadFile(genOne)
	if err != nil {
		t.Fatalf("read %s after the second install: %v", genOne, err)
	}
	if string(after) != "one" {
		t.Errorf("gen/1 now resolves to %q, want %q: the second "+
			"build rewrote bytes an existing generation reaches",
			after, "one")
	}
}

// TestLocalSourceGuardSeesEveryGeneration: the guard refuses a
// replacement that ANY generation reaches, not only the active one.
//
// `gale rollback` is why. It selects an old generation by number and
// expects the environment that generation described; a guard that
// consulted `current` alone would wave through a rebuild that
// rewrote the bytes every older generation resolves to, and the
// rollback would silently run the newest build.
func TestLocalSourceGuardSeesEveryGeneration(t *testing.T) {
	tmp := t.TempDir()
	storeRoot := filepath.Join(tmp, "store")
	galeDir := filepath.Join(tmp, "gale-home")

	// The store dir the old generation links, and a newer one the
	// active generation moved on to.
	oldDir := filepath.Join(storeRoot, "testpkg", "1.0.0-1", "bin")
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(oldDir, "testpkg"), []byte("old"), 0o755,
	); err != nil {
		t.Fatal(err)
	}
	newDir := filepath.Join(storeRoot, "testpkg", "2.0.0-1", "bin")
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(newDir, "testpkg"), []byte("new"), 0o755,
	); err != nil {
		t.Fatal(err)
	}

	// gen/1 links the old version; gen/2 is current and links the
	// new one.
	linkGen(t, galeDir, 1, filepath.Join(oldDir, "testpkg"))
	linkGen(t, galeDir, 2, filepath.Join(newDir, "testpkg"))
	if err := os.Symlink(
		filepath.Join("gen", "2"), filepath.Join(galeDir, "current"),
	); err != nil {
		t.Fatal(err)
	}

	ctx := &cmdContext{GaleDir: galeDir, StoreRoot: storeRoot}
	guard := localSourceReplaceGuard(ctx, &recipe.Recipe{
		Package: recipe.Package{Name: "testpkg", Version: "1.0.0"},
	})

	err := guard(installer.Replacement{
		CanonicalDir: filepath.Join(storeRoot, "testpkg", "1.0.0-1"),
	})
	if !errors.Is(err, errGenerationReferenced) {
		t.Errorf("err = %v, want a refusal: gen/1 still links "+
			"testpkg@1.0.0-1 even though gen/2 is current", err)
	}

	// A dependency committed underneath this build is not the
	// package the command named, and the guard says nothing about it.
	if err := guard(installer.Replacement{
		CanonicalDir: filepath.Join(storeRoot, "somedep", "1.0.0-1"),
	}); err != nil {
		t.Errorf("guard refused an unrelated dependency: %v", err)
	}
}

// linkGen creates gen/<n>/bin/testpkg pointing at target.
func linkGen(t *testing.T, galeDir string, n int, target string) {
	t.Helper()
	binDir := filepath.Join(galeDir, "gen", strconv.Itoa(n), "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(binDir, "testpkg")); err != nil {
		t.Fatal(err)
	}
}
