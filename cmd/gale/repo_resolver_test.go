package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/recipe"
)

// stubResolver returns a RecipeResolver that recognizes a small
// fixture map. Names absent from the map produce an error so
// composeResolvers can exercise its fallthrough.
func stubResolver(t *testing.T, fixtures map[string]string) installer.RecipeResolver {
	t.Helper()
	return func(name string) (*recipe.Recipe, error) {
		body, ok := fixtures[name]
		if !ok {
			return nil, fmt.Errorf("stub: %q not found", name)
		}
		return recipe.Parse(body)
	}
}

func jqRecipe(version string) string {
	return fmt.Sprintf(
		"[package]\nname = \"jq\"\nversion = %q\n"+
			"\n[source]\nurl = \"https://example.com/jq.tar.gz\"\n"+
			"sha256 = \"0000000000000000000000000000000000000000000000000000000000000000\"\n"+
			"\n[build]\nsteps = [\"true\"]\n",
		version)
}

// --- composeResolvers ---

func TestComposeResolversFirstHitWins(t *testing.T) {
	first := stubResolver(t, map[string]string{
		"jq": jqRecipe("1.0.0"),
	})
	secondCalled := false
	second := installer.RecipeResolver(func(name string) (*recipe.Recipe, error) {
		secondCalled = true
		return nil, fmt.Errorf("second should not be consulted")
	})

	r := composeResolvers(first, second)
	got, err := r("jq")
	if err != nil {
		t.Fatalf("composeResolvers: %v", err)
	}
	if got.Package.Version != "1.0.0" {
		t.Errorf("Version = %q, want 1.0.0", got.Package.Version)
	}
	if secondCalled {
		t.Error("second resolver consulted after first hit")
	}
}

func TestComposeResolversFallsThroughOnMiss(t *testing.T) {
	first := stubResolver(t, map[string]string{})
	second := stubResolver(t, map[string]string{
		"jq": jqRecipe("2.0.0"),
	})
	r := composeResolvers(first, second)
	got, err := r("jq")
	if err != nil {
		t.Fatalf("composeResolvers: %v", err)
	}
	if got.Package.Version != "2.0.0" {
		t.Errorf("Version = %q, want 2.0.0", got.Package.Version)
	}
}

func TestComposeResolversReturnsLastErrorOnAllMisses(t *testing.T) {
	first := installer.RecipeResolver(func(name string) (*recipe.Recipe, error) {
		return nil, errors.New("first error")
	})
	second := installer.RecipeResolver(func(name string) (*recipe.Recipe, error) {
		return nil, errors.New("second error: registry HTTP 502")
	})
	r := composeResolvers(first, second)
	_, err := r("jq")
	if err == nil {
		t.Fatal("expected error from all-miss compose")
	}
	// Last error wins so fallback diagnostics surface.
	if !strings.Contains(err.Error(), "second error") {
		t.Errorf("error = %q, want containing 'second error'", err)
	}
}

func TestComposeResolversEmptyReturnsError(t *testing.T) {
	r := composeResolvers()
	_, err := r("jq")
	if err == nil {
		t.Fatal("expected error from empty compose")
	}
}

// --- configuredRepoResolvers ---

func setupTapCache(t *testing.T, galeDir, tapName string, recipes map[string]string) {
	t.Helper()
	for fname, body := range recipes {
		letter := string(fname[0])
		dir := filepath.Join(
			galeDir, "repos", tapName, "recipes", letter)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir tap recipes: %v", err)
		}
		p := filepath.Join(dir, fname+".toml")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write tap recipe: %v", err)
		}
	}
}

func writeAppConfig(t *testing.T, galeDir, body string) {
	t.Helper()
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatalf("mkdir gale dir: %v", err)
	}
	p := filepath.Join(galeDir, "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
}

func TestConfiguredRepoResolversNoConfigFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	resolvers, err := configuredRepoResolvers()
	if err != nil {
		t.Fatalf("configuredRepoResolvers: %v", err)
	}
	if len(resolvers) != 0 {
		t.Errorf("len(resolvers) = %d, want 0", len(resolvers))
	}
}

func TestConfiguredRepoResolversNoReposEntry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	galeDir := filepath.Join(home, ".gale")
	writeAppConfig(t, galeDir, "[registry]\nurl = \"https://example.com\"\n")

	resolvers, err := configuredRepoResolvers()
	if err != nil {
		t.Fatalf("configuredRepoResolvers: %v", err)
	}
	if len(resolvers) != 0 {
		t.Errorf("len(resolvers) = %d, want 0", len(resolvers))
	}
}

func TestConfiguredRepoResolversFindsRecipeFromTap(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	galeDir := filepath.Join(home, ".gale")
	setupTapCache(t, galeDir, "mytap", map[string]string{
		"jq": jqRecipe("9.9.9"),
	})
	writeAppConfig(t, galeDir,
		"[[repos]]\nname = \"mytap\"\n"+
			"url = \"https://example.com/mytap.git\"\npriority = 1\n")

	resolvers, err := configuredRepoResolvers()
	if err != nil {
		t.Fatalf("configuredRepoResolvers: %v", err)
	}
	if len(resolvers) != 1 {
		t.Fatalf("len(resolvers) = %d, want 1", len(resolvers))
	}
	got, err := resolvers[0]("jq")
	if err != nil {
		t.Fatalf("tap resolver: %v", err)
	}
	if got.Package.Version != "9.9.9" {
		t.Errorf("Version = %q, want 9.9.9", got.Package.Version)
	}
}

func TestConfiguredRepoResolversSortedByPriority(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	galeDir := filepath.Join(home, ".gale")
	// Two taps with the SAME recipe at different versions —
	// the priority-1 tap must come first in the resolver slice
	// regardless of declaration order.
	setupTapCache(t, galeDir, "low-pri", map[string]string{
		"jq": jqRecipe("5.5.5"),
	})
	setupTapCache(t, galeDir, "high-pri", map[string]string{
		"jq": jqRecipe("1.1.1"),
	})
	writeAppConfig(t, galeDir,
		"[[repos]]\nname = \"low-pri\"\n"+
			"url = \"https://example.com/low.git\"\npriority = 5\n"+
			"\n[[repos]]\nname = \"high-pri\"\n"+
			"url = \"https://example.com/high.git\"\npriority = 1\n")

	resolvers, err := configuredRepoResolvers()
	if err != nil {
		t.Fatalf("configuredRepoResolvers: %v", err)
	}
	if len(resolvers) != 2 {
		t.Fatalf("len(resolvers) = %d, want 2", len(resolvers))
	}
	got, err := resolvers[0]("jq")
	if err != nil {
		t.Fatalf("first resolver: %v", err)
	}
	if got.Package.Version != "1.1.1" {
		t.Errorf("first resolver returned %q, want 1.1.1 (high-pri)",
			got.Package.Version)
	}
}

func TestConfiguredRepoResolversSkipsMissingCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	galeDir := filepath.Join(home, ".gale")
	// "ghost" repo is in config but never cloned — no cache dir.
	// "real" repo has a cache dir with recipes.
	setupTapCache(t, galeDir, "real", map[string]string{
		"jq": jqRecipe("3.0.0"),
	})
	writeAppConfig(t, galeDir,
		"[[repos]]\nname = \"ghost\"\n"+
			"url = \"https://example.com/ghost.git\"\npriority = 1\n"+
			"\n[[repos]]\nname = \"real\"\n"+
			"url = \"https://example.com/real.git\"\npriority = 2\n")

	resolvers, err := configuredRepoResolvers()
	if err != nil {
		t.Fatalf("configuredRepoResolvers: %v", err)
	}
	if len(resolvers) != 1 {
		t.Fatalf("len(resolvers) = %d, want 1 (ghost should be skipped)",
			len(resolvers))
	}
	got, err := resolvers[0]("jq")
	if err != nil {
		t.Fatalf("real resolver: %v", err)
	}
	if got.Package.Version != "3.0.0" {
		t.Errorf("Version = %q, want 3.0.0", got.Package.Version)
	}
}

// --- end-to-end through resolveRecipeResolver ---

func TestResolveRecipeResolverWithRecipesFlagSkipsTaps(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	galeDir := filepath.Join(home, ".gale")
	// Tap that would intercept "jq" if consulted.
	setupTapCache(t, galeDir, "intercept", map[string]string{
		"jq": jqRecipe("9.9.9"),
	})
	writeAppConfig(t, galeDir,
		"[[repos]]\nname = \"intercept\"\n"+
			"url = \"https://example.com/intercept.git\"\npriority = 1\n")

	// Flag-provided recipes dir takes precedence.
	flagDir := t.TempDir()
	letterDir := filepath.Join(flagDir, "j")
	if err := os.MkdirAll(letterDir, 0o755); err != nil {
		t.Fatalf("mkdir flag recipes: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(letterDir, "jq.toml"),
		[]byte(jqRecipe("1.2.3")),
		0o644); err != nil {
		t.Fatalf("write flag recipe: %v", err)
	}

	resolver, reg, err := resolveRecipeResolver(flagDir, "")
	if err != nil {
		t.Fatalf("resolveRecipeResolver: %v", err)
	}
	if reg != nil {
		t.Error("registry should be nil when --recipes is set")
	}
	got, err := resolver("jq")
	if err != nil {
		t.Fatalf("resolver: %v", err)
	}
	// Flag wins, tap is bypassed.
	if got.Package.Version != "1.2.3" {
		t.Errorf("Version = %q, want 1.2.3 (flag-provided)",
			got.Package.Version)
	}
}

func TestResolveRecipeResolverPrefersTapOverRegistry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	galeDir := filepath.Join(home, ".gale")
	setupTapCache(t, galeDir, "mytap", map[string]string{
		"jq": jqRecipe("7.7.7"),
	})
	writeAppConfig(t, galeDir,
		"[[repos]]\nname = \"mytap\"\n"+
			"url = \"https://example.com/mytap.git\"\npriority = 1\n")

	resolver, reg, err := resolveRecipeResolver("", "")
	if err != nil {
		t.Fatalf("resolveRecipeResolver: %v", err)
	}
	if reg == nil {
		t.Fatal("registry should still be available for versioned fetches")
	}
	got, err := resolver("jq")
	if err != nil {
		t.Fatalf("resolver: %v", err)
	}
	if got.Package.Version != "7.7.7" {
		t.Errorf("Version = %q, want 7.7.7 (tap wins)",
			got.Package.Version)
	}
}
