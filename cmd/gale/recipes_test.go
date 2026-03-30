package main

import "testing"

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

func TestDetectRecipesRepoWithMultiCharBucket(t *testing.T) {
	// Letter bucket must be single character.
	path := "/home/user/code/gale-recipes/recipes/jq/jq.toml"
	got := detectRecipesRepo(path)
	if got != "" {
		t.Errorf("detectRecipesRepo(%q) = %q, want empty",
			path, got)
	}
}
