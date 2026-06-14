package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectRecipesRepoWithValidPath(t *testing.T) {
	path := "/home/user/code/gale-recipes/recipes/j/jq.toml"
	got := detectRecipesRepo(path)
	want := "/home/user/code/gale-recipes/recipes"
	if got != want {
		t.Errorf("detectRecipesRepo(%q) = %q, want %q",
			path, got, want)
	}
}

func TestDetectRecipesRepoWithNoRecipesDir(t *testing.T) {
	path := "/home/user/code/jq.toml"
	got := detectRecipesRepo(path)
	if got != "" {
		t.Errorf("detectRecipesRepo(%q) = %q, want empty",
			path, got)
	}
}

func TestFindLocalRecipesDirOverride(t *testing.T) {
	// Create a temp dir with a recipes/ subdirectory.
	tmp := t.TempDir()
	recipesSubdir := filepath.Join(tmp, "recipes")
	if err := os.MkdirAll(recipesSubdir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Override with a path that contains recipes/ subdir.
	got, err := findLocalRecipesDir(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != recipesSubdir {
		t.Errorf("got %q, want %q", got, recipesSubdir)
	}

	// Override with a path that has no recipes/ subdir — use
	// the path directly.
	plain := t.TempDir()
	got, err = findLocalRecipesDir(plain)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != plain {
		t.Errorf("got %q, want %q", got, plain)
	}
}

func TestFindLocalRecipesDirRejectsMissingDir(t *testing.T) {
	// A nonexistent --recipes path must fail fast with one
	// clear error, not flow into misleading per-package
	// "no local recipe" misses (gh#114). This also catches
	// `--recipes --build`, where pflag consumes the next
	// flag as the value.
	missing := filepath.Join(t.TempDir(), "nope")
	_, err := findLocalRecipesDir(missing)
	if err == nil {
		t.Fatal("expected error for nonexistent directory, got nil")
	}
	if !strings.Contains(err.Error(), "recipes directory not found") {
		t.Errorf("error %q does not name the missing directory", err)
	}
}

func TestFindLocalRecipesDirRejectsNonDirectory(t *testing.T) {
	// A --recipes path that names a file is an error.
	file := filepath.Join(t.TempDir(), "recipes.toml")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := findLocalRecipesDir(file)
	if err == nil {
		t.Fatal("expected error for non-directory, got nil")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("error %q does not say the path is not a directory", err)
	}
}

func TestFindLocalRecipesDirRejectsEmptyOverride(t *testing.T) {
	// The bare --recipes form and its sibling ../gale-recipes/
	// fallback were removed (gh#114). An empty override is an
	// error.
	if _, err := findLocalRecipesDir(""); err == nil {
		t.Fatal("expected error for empty override, got nil")
	}
}

func TestRecipeFileResolverNeverReturnsNil(t *testing.T) {
	// recipeFileResolver must never return nil, even for
	// paths that can't be resolved. It should return a
	// resolver that produces an error instead.
	resolver := recipeFileResolver("")
	if resolver == nil {
		t.Fatal("recipeFileResolver returned nil")
	}
	_, err := resolver("jq")
	if err == nil {
		t.Error("expected error from resolver with invalid path")
	}
}

func TestLocalRecipeResolverEmptyName(t *testing.T) {
	resolver := localRecipeResolver(t.TempDir())
	_, err := resolver("")
	if err == nil {
		t.Fatal("expected error for empty package name")
	}
}

func TestDetectRecipesRepoWithMultiCharBucket(t *testing.T) {
	// Letter bucket must be single character.
	path := "/home/user/code/gale-recipes/recipes/jq/jq.toml"
	got := detectRecipesRepo(path)
	if got != "" {
		t.Errorf("detectRecipesRepo(%q) = %q, want empty",
			path, got)
	}
}
