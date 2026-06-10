package main

// Tests for unit U12 (resolver-misc):
//
//   gh#70 — `gale install pkg@version` must resolve through the
//   configured tap chain (taps first, registry fallback) like
//   every other version-aware command, instead of dialing the
//   registry directly.
//
//   gh#71 — composeResolvers must distinguish a genuine miss
//   (recipe not in this source) from a real failure (recipe
//   exists but is corrupt or unreadable). Real failures stop
//   the chain and surface; misses fall through.
//
// Helpers setupTapCache, writeAppConfig, jqRecipe, and
// stubResolver are shared with repo_resolver_test.go (same
// package).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/recipe"
)

// resetInstallFlags snapshots the package-level install flag
// variables and restores them when the test ends, then sets
// them to their defaults.
func resetInstallFlags(t *testing.T) {
	t.Helper()
	savedGlobal := installGlobal
	savedProject := installProject
	savedRecipes := installRecipes
	savedRecipe := installRecipe
	savedPath := installPath
	savedGit := installGit
	savedBuild := installBuild
	savedHost := installHost
	savedDryRun := dryRun
	t.Cleanup(func() {
		installGlobal = savedGlobal
		installProject = savedProject
		installRecipes = savedRecipes
		installRecipe = savedRecipe
		installPath = savedPath
		installGit = savedGit
		installBuild = savedBuild
		installHost = savedHost
		dryRun = savedDryRun
	})
	installGlobal = false
	installProject = false
	installRecipes = ""
	installRecipe = ""
	installPath = ""
	installGit = false
	installBuild = false
	installHost = ""
}

// gh#70: a tap-only (or tap-overridden) package must be
// version-pinnable. The tap holds jq@1.0.0 and the registry is
// unreachable; before the fix the versioned install branch
// called newRegistry().FetchRecipeVersion directly, bypassed
// the tap, and failed with connection refused.
func TestInstallVersionedConsultsTapChain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir()) // no project gale.toml -> global scope

	galeDir := filepath.Join(home, ".gale")
	setupTapCache(t, galeDir, "mytap", map[string]string{
		"jq": jqRecipe("1.0.0"),
	})
	// Registry dials fail fast: nothing listens on port 1.
	writeAppConfig(t, galeDir,
		"[registry]\nurl = \"http://127.0.0.1:1\"\n"+
			"\n[[repos]]\nname = \"mytap\"\n"+
			"url = \"https://example.com/mytap.git\"\npriority = 1\n")

	resetInstallFlags(t)
	dryRun = true

	err := installCmd.RunE(installCmd, []string{"jq@1.0.0"})
	if err != nil {
		t.Fatalf("install jq@1.0.0 should resolve from the tap, got: %v", err)
	}
}

// gh#71: a corrupt recipe in a higher-priority tap must surface
// as an error naming the failing file — not silently fall
// through to the registry copy.
func TestComposeResolversSurfacesCorruptTapRecipe(t *testing.T) {
	recipesDir := t.TempDir()
	letterDir := filepath.Join(recipesDir, "j")
	if err := os.MkdirAll(letterDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Unterminated string — recipe.Parse must fail.
	corrupt := "[package]\nname = \"jq\nversion = \"1.0.0\"\n"
	if err := os.WriteFile(
		filepath.Join(letterDir, "jq.toml"), []byte(corrupt), 0o644,
	); err != nil {
		t.Fatalf("write corrupt recipe: %v", err)
	}

	tap := localRecipeResolver(recipesDir)
	registryConsulted := false
	registryStub := installer.RecipeResolver(
		func(name string) (*recipe.Recipe, error) {
			registryConsulted = true
			return recipe.Parse(jqRecipe("9.9.9"))
		},
	)

	r := composeResolvers(tap, registryStub)
	_, err := r("jq")
	if err == nil {
		t.Fatal("corrupt tap recipe silently fell through to the registry")
	}
	if registryConsulted {
		t.Error("registry consulted after a real tap failure")
	}
	if !strings.Contains(err.Error(), "jq.toml") {
		t.Errorf("error should name the failing recipe file, got: %v", err)
	}
}

// gh#71: an existing-but-unreadable tap recipe is a real failure,
// not a miss — the actionable error must surface instead of the
// registry's copy (or its not-found error) masking it.
func TestComposeResolversSurfacesUnreadableTapRecipe(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file modes")
	}
	recipesDir := t.TempDir()
	letterDir := filepath.Join(recipesDir, "j")
	if err := os.MkdirAll(letterDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(letterDir, "jq.toml")
	if err := os.WriteFile(path, []byte(jqRecipe("1.0.0")), 0o644); err != nil {
		t.Fatalf("write recipe: %v", err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	tap := localRecipeResolver(recipesDir)
	registryStub := stubResolver(t, map[string]string{
		"jq": jqRecipe("9.9.9"),
	})

	_, err := composeResolvers(tap, registryStub)("jq")
	if err == nil {
		t.Fatal("unreadable tap recipe silently fell through to the registry")
	}
}

// gh#71 regression guard: a genuine miss (recipe absent from the
// tap) must still fall through to the next resolver.
func TestComposeResolversTapMissStillFallsThrough(t *testing.T) {
	tap := localRecipeResolver(t.TempDir()) // empty dir -> miss
	registryStub := stubResolver(t, map[string]string{
		"jq": jqRecipe("2.0.0"),
	})

	got, err := composeResolvers(tap, registryStub)("jq")
	if err != nil {
		t.Fatalf("miss should fall through to registry, got: %v", err)
	}
	if got.Package.Version != "2.0.0" {
		t.Errorf("Version = %q, want 2.0.0 (registry)", got.Package.Version)
	}
}
