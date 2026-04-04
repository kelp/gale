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
		{"@invalid", "@invalid", ""},
	}

	for _, tt := range tests {
		name, version := parsePackageArg(tt.input)
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

func TestNewInstallerForRecipeFile(t *testing.T) {
	// Verify that newInstallerForRecipeFile returns an
	// Installer with a non-nil Verifier so Sigstore
	// attestation is checked for binary installs.
	storeRoot := t.TempDir()
	inst := newInstallerForRecipeFile(
		"/tmp/recipes/j/jq.toml", storeRoot)
	if inst.Verifier == nil {
		t.Fatal("Verifier is nil — attestation will be " +
			"silently skipped")
	}
}

func TestNewInstallerForLocalSource(t *testing.T) {
	// Verify that newInstallerForLocalSource returns an
	// Installer with a non-nil Verifier so Sigstore
	// attestation is checked for dep installs.
	storeRoot := t.TempDir()
	inst := newInstallerForLocalSource(
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
	err := installFromLocalSource("testpkg", recipePath,
		srcDir, configPath, galeDir, storeRoot, out)
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
