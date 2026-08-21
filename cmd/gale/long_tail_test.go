package main

import "testing"

func TestLongTailCommandsGone(t *testing.T) {
	gone := []string{
		"build",
		"create-recipe",
		"audit",
		"search",
		"switch",
		"add",
		"sbom",
		"inspect",
		"repo",
	}
	for _, name := range gone {
		if findCmd(name) != nil {
			t.Errorf("%s: still registered", name)
		}
	}
	if findSub("repo", "add") != nil {
		t.Error("repo add: still registered")
	}
	if findCmd("lint") == nil {
		t.Fatal("lint: must stay")
	}
}

func TestLintStrictFlagGone(t *testing.T) {
	cmd := findCmd("lint")
	if cmd == nil {
		t.Fatal("lint: must stay")
	}
	if cmd.Flags().Lookup("strict") != nil {
		t.Fatal("lint --strict must be gone with recipe lint")
	}
}
