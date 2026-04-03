package main

import (
	"os"
	"path/filepath"
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
	got, err := findLocalRecipesDir("ignored", tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != recipesSubdir {
		t.Errorf("got %q, want %q", got, recipesSubdir)
	}

	// Override with a path that has no recipes/ subdir — use
	// the path directly.
	plain := t.TempDir()
	got, err = findLocalRecipesDir("ignored", plain)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != plain {
		t.Errorf("got %q, want %q", got, plain)
	}
}

func TestFindLocalRecipesDirAutoDetect(t *testing.T) {
	// Create sibling structure: parent/gale-recipes/recipes/
	parent := t.TempDir()
	recipesDir := filepath.Join(
		parent, "gale-recipes", "recipes")
	if err := os.MkdirAll(recipesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Simulate cwd as parent/gale (sibling of gale-recipes).
	cwd := filepath.Join(parent, "gale")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}

	// Empty override triggers auto-detect.
	got, err := findLocalRecipesDir(cwd, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != recipesDir {
		t.Errorf("got %q, want %q", got, recipesDir)
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
