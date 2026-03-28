package main

import "testing"

func TestDetectRecipesRepoWithValidPath(t *testing.T) {
	path := "/home/user/code/gale-recipes/recipes/j/jq.toml"
	got := detectRecipesRepo(path)
	if got != "/home/user/code/gale-recipes" {
		t.Errorf("detectRecipesRepo(%q) = %q, want %q",
			path, got, "/home/user/code/gale-recipes")
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

func TestDetectRecipesRepoWithMultiCharBucket(t *testing.T) {
	// Letter bucket must be single character.
	path := "/home/user/code/gale-recipes/recipes/jq/jq.toml"
	got := detectRecipesRepo(path)
	if got != "" {
		t.Errorf("detectRecipesRepo(%q) = %q, want empty",
			path, got)
	}
}
