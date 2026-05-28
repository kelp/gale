package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/output"
	"github.com/spf13/cobra"
)

func TestParsePackageArg(t *testing.T) {
	tests := []struct {
		input       string
		wantName    string
		wantVersion string
	}{
		{"jq", "jq", ""},
		{"python@3.11", "python", "3.11"},
		{"node@20", "node", "20"},
		{"ripgrep@latest", "ripgrep", "latest"},
		// Leading "@" without a separator after it has no
		// version segment — the caller's name validation
		// (registry.ValidName) rejects this downstream. The
		// parser must not invent a version.
		{"@invalid", "@invalid", ""},
		// Revision suffix is part of the version segment.
		{"hello@1.0-1", "hello", "1.0-1"},
	}

	for _, tt := range tests {
		name, version, err := parsePackageArg(tt.input)
		if err != nil {
			t.Errorf("parsePackageArg(%q) returned error: %v",
				tt.input, err)
			continue
		}
		if name != tt.wantName {
			t.Errorf("parsePackageArg(%q) name = %q, want %q",
				tt.input, name, tt.wantName)
		}
		if version != tt.wantVersion {
			t.Errorf("parsePackageArg(%q) version = %q, want %q",
				tt.input, version, tt.wantVersion)
		}
	}
}

// TestParsePackageArgRejectsBadInput pins the parser's
// strict contract for malformed <name>[@version] strings.
// Each case in this table previously fell through to the
// "latest" branch in install/update/info, silently ignoring
// the user's pin. See finding F-1.
func TestParsePackageArgRejectsBadInput(t *testing.T) {
	tests := []struct {
		input string
		// substr must appear somewhere in the returned error
		// so callers and users see the actual problem.
		substr string
	}{
		{"name@", "empty version"},
		// Multiple "@" wins over the empty-version check since
		// it indicates a more fundamental shape problem.
		{"name@@", "multiple"},
		{"name@@1.0", "multiple"},
		{"foo@bar@baz", "multiple"},
		{"name@1 0", "whitespace"},
		{"name@1\t0", "whitespace"},
		{"name@1\n0", "whitespace"},
		{"name@1;rm", "invalid character"},
		{"name@1|cat", "invalid character"},
		{"name@1&", "invalid character"},
		{"name@1$x", "invalid character"},
		{"name@1`x`", "invalid character"},
		{"name@1\x00", "invalid character"},
	}

	for _, tt := range tests {
		_, _, err := parsePackageArg(tt.input)
		if err == nil {
			t.Errorf("parsePackageArg(%q) = nil error, want error containing %q",
				tt.input, tt.substr)
			continue
		}
		if !strings.Contains(err.Error(), tt.substr) {
			t.Errorf("parsePackageArg(%q) error = %q, want substring %q",
				tt.input, err.Error(), tt.substr)
		}
	}
}

func TestInstallHasHostFlag(t *testing.T) {
	f := installCmd.Flags().Lookup("host")
	if f == nil {
		t.Fatal("install: --host flag not found")
	}
	if f.DefValue != "" {
		t.Errorf("install: --host default = %q, want empty",
			f.DefValue)
	}
}

func TestValidateInstallFlags(t *testing.T) {
	tests := []struct {
		name    string
		global  bool
		project bool
		wantErr bool
	}{
		{"neither flag is ok", false, false, false},
		{"global only is ok", true, false, false},
		{"project only is ok", false, true, false},
		{"both flags errors", true, true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateInstallFlags(tt.global, tt.project)
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestFormatDevVersion(t *testing.T) {
	tests := []struct {
		name     string
		describe string
		want     string
	}{
		{
			"on tag",
			"v0.2.0",
			"0.2.0",
		},
		{
			"commits ahead of tag",
			"v0.2.0-7-g5395b8f",
			"0.2.0-dev.7+5395b8f",
		},
		{
			"no tags, bare hash",
			"5395b8f",
			"0.0.0-dev+5395b8f",
		},
		{
			"one commit ahead",
			"v1.0.0-1-gabcdef0",
			"1.0.0-dev.1+abcdef0",
		},
		{
			"pre-release tag",
			"v1.0.0-rc1",
			"1.0.0-rc1",
		},
		{
			"pre-release tag with commits ahead",
			"v1.0.0-rc1-3-gabcdef0",
			"1.0.0-rc1-dev.3+abcdef0",
		},
		{
			"pre-release tag alpha",
			"v2.0.0-alpha.1",
			"2.0.0-alpha.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatDevVersion(tt.describe)
			if got != tt.want {
				t.Errorf("formatDevVersion(%q) = %q, want %q",
					tt.describe, got, tt.want)
			}
		})
	}
}

func TestRecipesFlagReplacesLocal(t *testing.T) {
	cmds := map[string]*cobra.Command{
		"install":  installCmd,
		"add":      addCmd,
		"update":   updateCmd,
		"sync":     syncCmd,
		"outdated": outdatedCmd,
	}

	for name, cmd := range cmds {
		t.Run(name, func(t *testing.T) {
			// --recipes must exist.
			f := cmd.Flags().Lookup("recipes")
			if f == nil {
				t.Fatalf("%s: --recipes flag not found", name)
			}
			if f.DefValue != "" {
				t.Errorf("%s: --recipes default = %q, want empty",
					name, f.DefValue)
			}
			if f.NoOptDefVal != "auto" {
				t.Errorf("%s: --recipes NoOptDefVal = %q, want %q",
					name, f.NoOptDefVal, "auto")
			}

			// --local must not exist.
			if cmd.Flags().Lookup("local") != nil {
				t.Errorf("%s: --local flag should not exist",
					name)
			}
		})
	}
}

// TestRecipesFlagWordingIsAccurate verifies the --recipes
// flag description across every command that exposes it. The
// previous wording — "Use local recipes directory (default:
// ../gale-recipes/)" — was inaccurate: with no flag, recipes
// resolve through the remote registry, not a sibling. The
// sibling path only applies when --recipes is passed bare.
//
// See finding F-2 (and the original fix on outdated in
// commit 4a54c9e).
func TestRecipesFlagWordingIsAccurate(t *testing.T) {
	cmds := map[string]*cobra.Command{
		"install":  installCmd,
		"add":      addCmd,
		"update":   updateCmd,
		"sync":     syncCmd,
		"outdated": outdatedCmd,
		"gc":       gcCmd,
		"inspect":  inspectCmd,
		"switch":   switchCmd,
		"build":    buildCmd,
	}

	for name, cmd := range cmds {
		t.Run(name, func(t *testing.T) {
			f := cmd.Flags().Lookup("recipes")
			if f == nil {
				t.Fatalf("%s: --recipes flag not found", name)
			}
			// The old wording implied the sibling path was the
			// default with no flag, which it is not. Reject it.
			if strings.Contains(f.Usage,
				"Use local recipes directory (default:") {
				t.Errorf("%s: --recipes still uses the old "+
					"misleading wording: %q", name, f.Usage)
			}
			// The new wording must say "instead of the registry"
			// so it is clear what the default behavior is.
			if !strings.Contains(f.Usage, "instead of the registry") {
				t.Errorf("%s: --recipes usage %q does not "+
					"clarify it overrides the registry",
					name, f.Usage)
			}
		})
	}
}

func TestPathFlagReplacesSource(t *testing.T) {
	cmds := map[string]*cobra.Command{
		"install": installCmd,
		"update":  updateCmd,
	}

	for name, cmd := range cmds {
		t.Run(name, func(t *testing.T) {
			if cmd.Flags().Lookup("path") == nil {
				t.Errorf("%s: --path flag not found", name)
			}
			if cmd.Flags().Lookup("source") != nil {
				t.Errorf(
					"%s: --source flag should not exist",
					name)
			}
		})
	}
}

func TestResolveScope(t *testing.T) {
	// Create a temp dir with a gale.toml for project detection.
	tmp := t.TempDir()
	galePath := filepath.Join(tmp, "gale.toml")
	if err := os.WriteFile(galePath, []byte("[packages]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		global     bool
		project    bool
		cwd        string
		wantGlobal bool
	}{
		{
			"-g flag forces global",
			true, false, tmp, true,
		},
		{
			"-p flag forces project",
			false, true, tmp, false,
		},
		{
			"no flags no gale.toml defaults global",
			false, false, t.TempDir(), true,
		},
		{
			"no flags with gale.toml defaults project",
			false, false, tmp, false,
		},
		{
			"no flags with .tool-versions defaults project",
			false, false, func() string {
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, ".tool-versions"), []byte("golang 1.26.1\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				return dir
			}(), false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveScope(tt.global, tt.project,
				tt.cwd)
			if got != tt.wantGlobal {
				t.Errorf("resolveScope() = %v, want %v",
					got, tt.wantGlobal)
			}
		})
	}
}

func TestNewInstallerForRecipe(t *testing.T) {
	// Verify that newInstallerForRecipe returns an
	// Installer with a non-nil Verifier so Sigstore
	// attestation is checked for binary installs.
	storeRoot := t.TempDir()
	inst := newInstallerForRecipe(
		"/tmp/recipes/j/jq.toml", storeRoot)
	if inst.Verifier == nil {
		t.Fatal("Verifier is nil — attestation will be " +
			"silently skipped")
	}
}

func TestResolverForRecipeInBucketedRepo(t *testing.T) {
	// When the recipe is inside a letter-bucketed repo,
	// resolverForRecipe should use the local repo resolver.
	tmp := t.TempDir()
	recipesDir := filepath.Join(tmp, "recipes", "j")
	if err := os.MkdirAll(recipesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	recipePath := filepath.Join(recipesDir, "jq.toml")
	recipeContent := strings.Join([]string{
		`[package]`,
		`name = "jq"`,
		`version = "1.7"`,
		``,
		`[source]`,
		`url = "https://example.com/jq-1.7.tar.gz"`,
		`sha256 = "deadbeef"`,
	}, "\n")
	if err := os.WriteFile(recipePath,
		[]byte(recipeContent), 0o644); err != nil {
		t.Fatal(err)
	}

	resolver := resolverForRecipe(recipePath)

	// The resolver should be able to find jq.toml by name.
	r, err := resolver("jq")
	if err != nil {
		t.Fatalf("resolver failed for jq: %v", err)
	}
	if r.Package.Name != "jq" {
		t.Errorf("got name %q, want %q", r.Package.Name, "jq")
	}
}

func TestResolverForRecipeNotInBucketedRepo(t *testing.T) {
	// When the recipe is NOT inside a letter-bucketed repo,
	// resolverForRecipe should fall back to a non-nil resolver
	// that doesn't panic or produce a wrong path.
	tmp := t.TempDir()
	recipePath := filepath.Join(tmp, "custom.toml")
	if err := os.WriteFile(recipePath,
		[]byte("[package]\nname = \"custom\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	resolver := resolverForRecipe(recipePath)
	if resolver == nil {
		t.Fatal("resolver is nil for non-bucketed recipe")
	}
}

func TestInstallFromGitResolverFallback(t *testing.T) {
	// When --recipe points to a non-bucketed path,
	// installFromGit should use a registry resolver for
	// dep resolution instead of the broken
	// recipeFileResolver.
	tmp := t.TempDir()
	recipePath := filepath.Join(tmp, "custom.toml")
	if err := os.WriteFile(recipePath,
		[]byte("[package]\nname = \"custom\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	resolver := resolverForRecipe(recipePath)
	if resolver == nil {
		t.Fatal("resolver is nil for non-bucketed recipe")
	}

	// Verify it doesn't point to a nonsense directory.
	// With the old recipeFileResolver, navigating up 3 dirs
	// from /tmp/xxx/custom.toml would compute a wrong path.
	// The fallback should return a working resolver.
	_, err := resolver("nonexistent-pkg-xyz")
	if err == nil {
		t.Fatal("expected error for nonexistent package")
	}
	// The error should come from a real resolver (registry or
	// local), not a file-not-found from a wrong directory.
}

// TestInstallRecipeFileWithVersionErrors verifies that
// combining --recipe (singular, a specific file) with an
// @version pin is rejected. The user already named the recipe
// file; an additional @version would be silently ignored.
//
// Finding F-5.3: --recipes (plural, a directory) is a local
// registry override and MUST accept @version. Only --recipe
// (singular, a file) rejects it.
func TestInstallRecipeFileWithVersionErrors(t *testing.T) {
	tmp := t.TempDir()
	recipePath := filepath.Join(tmp, "jq.toml")
	if err := os.WriteFile(recipePath,
		[]byte("[package]\nname = \"jq\"\nversion = \"1.7\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(orig) //nolint:errcheck

	// Save and restore all install package-level globals.
	savedRecipes := installRecipes
	savedGlobal := installGlobal
	savedProject := installProject
	savedPath := installPath
	savedGit := installGit
	savedBuild := installBuild
	savedRecipe := installRecipe
	defer func() {
		installRecipes = savedRecipes
		installGlobal = savedGlobal
		installProject = savedProject
		installPath = savedPath
		installGit = savedGit
		installBuild = savedBuild
		installRecipe = savedRecipe
	}()

	installRecipes = ""
	installRecipe = recipePath
	installGlobal = true
	installProject = false
	installPath = ""
	installGit = false
	installBuild = false

	err := installCmd.RunE(installCmd, []string{"jq@1.8.1"})
	if err == nil {
		t.Fatal("install --recipe jq.toml jq@1.8.1 must " +
			"return an error: @version is incompatible with --recipe")
	}
	msg := err.Error()
	if !strings.Contains(msg, "version") || !strings.Contains(msg, "--recipe") {
		t.Errorf("error %q does not mention version + --recipe "+
			"incompatibility", msg)
	}
}

// TestInstallFromRecipeFileRotatesGeneration is a regression
// test for gale#22 [sic: gh#23]: `gale install -g <name>
// --recipe <file>` must rotate the active generation so
// current/bin/<name> resolves to the freshly installed
// revision. Before the fix, the lockfile got updated but
// the gen was left untouched — silent stale-PATH bug.
//
// Uses the MethodCached path (pre-populated store) so the
// test doesn't depend on a real build: install returns a
// "cached" result with empty SHA256, finalize runs anyway,
// and we assert generation.Build was reached and produced
// gen/1.
func TestInstallFromRecipeFileRotatesGeneration(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Recipe file (letter-bucketed so resolverForRecipe
	// recognizes it as a recipes-repo recipe).
	recipesDir := filepath.Join(tmp, "gale-recipes", "recipes", "t")
	if err := os.MkdirAll(recipesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	recipePath := filepath.Join(recipesDir, "testpkg.toml")
	// Source URL/SHA are required by recipe.Parse but never
	// fetched in this test — the pre-populated store dir
	// triggers MethodCached.
	recipeTOML := strings.Join([]string{
		`[package]`,
		`name = "testpkg"`,
		`version = "1.0.0"`,
		`revision = 1`,
		``,
		`[source]`,
		`url = "https://example.invalid/testpkg-1.0.0.tar.gz"`,
		`sha256 = "0000000000000000000000000000000000000000000000000000000000000000"`,
		``,
		`[build]`,
		`steps = ["true"]`,
	}, "\n")
	if err := os.WriteFile(recipePath,
		[]byte(recipeTOML), 0o644); err != nil {
		t.Fatal(err)
	}

	// Pre-populate the store at <version>-1/bin/testpkg so
	// IsInstalled returns true and install short-circuits
	// to MethodCached. Avoids needing a real source tarball.
	galeDir := filepath.Join(tmp, ".gale")
	storeRoot := filepath.Join(galeDir, "pkg")
	pkgBin := filepath.Join(storeRoot, "testpkg", "1.0.0-1", "bin")
	if err := os.MkdirAll(pkgBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(pkgBin, "testpkg"),
		[]byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Bootstrap an empty gale.toml.
	configPath := filepath.Join(galeDir, "gale.toml")
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := output.New(os.Stderr, false)
	ctx := &cmdContext{
		GalePath:  configPath,
		GaleDir:   galeDir,
		StoreRoot: storeRoot,
	}

	if err := installFromRecipeFile(ctx, recipePath, out); err != nil {
		t.Fatalf("installFromRecipeFile: %v", err)
	}

	// Contract: a new generation was built and current points
	// at it. Before the fix this was silently skipped.
	currentTarget, err := os.Readlink(filepath.Join(galeDir, "current"))
	if err != nil {
		t.Fatalf("readlink current: %v — install did not rotate the generation", err)
	}
	if currentTarget != filepath.Join("gen", "1") {
		t.Errorf("current = %q, want gen/1", currentTarget)
	}

	// And bin/testpkg in the new gen resolves to the store.
	symlinkPath := filepath.Join(galeDir, "gen", "1", "bin", "testpkg")
	target, err := os.Readlink(symlinkPath)
	if err != nil {
		t.Fatalf("readlink testpkg: %v", err)
	}
	wantSuffix := filepath.Join("testpkg", "1.0.0-1", "bin", "testpkg")
	if !strings.Contains(target, wantSuffix) {
		t.Errorf("testpkg symlink = %q, want suffix %q",
			target, wantSuffix)
	}
}

// TestInstallFromRecipeFileRotatesGenWhenOtherPackagesMissing
// is the precise repro for gh#23: the user runs
// `gale install -g <name> --recipe <file>` against a
// machine whose gale.toml lists OTHER packages whose store
// dirs aren't on this host yet (a fresh-clone-on-new-host
// scenario). Strict generation.Build errors on those, so
// the install for the target package "succeeds" in store
// + lockfile but the active generation is never rotated.
// User-visible: lockfile updated, store has new revision,
// but `which <name>` resolves the prior revision (or in
// the gen/308 case, a broken binary that segfaults).
//
// Fix: the install path uses lenient rebuild and verifies
// the target package landed on PATH. Other packages with
// missing store dirs no longer block this install from
// rotating the gen.
func TestInstallFromRecipeFileRotatesGenWhenOtherPackagesMissing(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Recipe file for the target package.
	recipesDir := filepath.Join(tmp, "gale-recipes", "recipes", "t")
	if err := os.MkdirAll(recipesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	recipePath := filepath.Join(recipesDir, "testpkg.toml")
	recipeTOML := strings.Join([]string{
		`[package]`,
		`name = "testpkg"`,
		`version = "1.0.0"`,
		`revision = 1`,
		``,
		`[source]`,
		`url = "https://example.invalid/testpkg-1.0.0.tar.gz"`,
		`sha256 = "0000000000000000000000000000000000000000000000000000000000000000"`,
		``,
		`[build]`,
		`steps = ["true"]`,
	}, "\n")
	if err := os.WriteFile(recipePath,
		[]byte(recipeTOML), 0o644); err != nil {
		t.Fatal(err)
	}

	// Pre-populate the store for testpkg so install short-
	// circuits to MethodCached.
	galeDir := filepath.Join(tmp, ".gale")
	storeRoot := filepath.Join(galeDir, "pkg")
	pkgBin := filepath.Join(storeRoot, "testpkg", "1.0.0-1", "bin")
	if err := os.MkdirAll(pkgBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(pkgBin, "testpkg"),
		[]byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// gale.toml lists testpkg AND another package whose
	// store dir is INTENTIONALLY missing — mirrors the
	// fresh-host scenario described in #23.
	configPath := filepath.Join(galeDir, "gale.toml")
	cfgTOML := strings.Join([]string{
		`[packages]`,
		`  testpkg = "1.0.0"`,
		`  missingpkg = "9.9.9"`,
		``,
	}, "\n")
	if err := os.WriteFile(configPath,
		[]byte(cfgTOML), 0o644); err != nil {
		t.Fatal(err)
	}

	out := output.New(os.Stderr, false)
	ctx := &cmdContext{
		GalePath:  configPath,
		GaleDir:   galeDir,
		StoreRoot: storeRoot,
	}

	if err := installFromRecipeFile(ctx, recipePath, out); err != nil {
		t.Fatalf("installFromRecipeFile: %v\n\n"+
			"This is the gh#23 failure: strict rebuild errors on "+
			"the unrelated missing 'missingpkg' store dir, blocking "+
			"the gen rotation that would put testpkg on PATH.",
			err)
	}

	// After the fix, current points to the new gen and
	// testpkg's symlink is there.
	currentTarget, err := os.Readlink(filepath.Join(galeDir, "current"))
	if err != nil {
		t.Fatalf("readlink current: %v — install did not rotate generation", err)
	}
	if currentTarget != filepath.Join("gen", "1") {
		t.Errorf("current = %q, want gen/1", currentTarget)
	}

	if _, err := os.Lstat(
		filepath.Join(galeDir, "gen", "1", "bin", "testpkg")); err != nil {
		t.Errorf("testpkg symlink missing in gen/1: %v", err)
	}
}

func TestInstallLocalFinalizesWhenStoreHasVersion(t *testing.T) {
	tmp := t.TempDir()

	// Create a git repo as the "source dir" so
	// gitDevVersion returns a stable version.
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
		{"config", "commit.gpgsign", "false"},
		{"commit", "--allow-empty", "-m", "init"},
		{"tag", "v1.0.0"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = srcDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v",
				args, string(out), err)
		}
	}

	// Create a recipe in a sibling gale-recipes dir.
	recipesDir := filepath.Join(tmp, "gale-recipes",
		"recipes", "t")
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
		`steps = ["true"]`,
	}, "\n")
	if err := os.WriteFile(recipePath, []byte(recipeTOML), 0o644); err != nil {
		t.Fatal(err)
	}

	// Pre-populate the store so IsInstalled returns true.
	storeRoot := filepath.Join(tmp, "store")
	pkgDir := filepath.Join(storeRoot, "testpkg", "1.0.0",
		"bin")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(pkgDir, "testpkg"),
		[]byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a gale dir and empty gale.toml.
	galeDir := filepath.Join(tmp, "gale-home")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(galeDir, "gale.toml")
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := output.New(os.Stderr, false)
	ctx := &cmdContext{
		GalePath:  configPath,
		GaleDir:   galeDir,
		StoreRoot: storeRoot,
	}
	err := installFromLocalSource(ctx, "testpkg", recipePath,
		srcDir, out)
	if err != nil {
		t.Fatalf("installFromLocalSource: %v", err)
	}

	// Verify the package was added to gale.toml.
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.ParseGaleConfig(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Packages["testpkg"]; !ok {
		t.Error("testpkg not added to gale.toml — " +
			"finalize was skipped")
	}
}
