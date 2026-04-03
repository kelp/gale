package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kelp/gale/internal/config"
)

// BUG-4: repo add doesn't persist to config.toml. After
// adding a repo, config.toml should contain the new entry.

func TestRepoAddPersistsToConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")

	// addRepoToConfig is the extracted persist function.
	err := addRepoToConfig(configPath, "test-repo",
		"https://example.com/recipes")
	if err != nil {
		t.Fatalf("addRepoToConfig error: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}

	cfg, err := config.ParseAppConfig(string(data))
	if err != nil {
		t.Fatalf("parsing config: %v", err)
	}

	if len(cfg.Repos) != 1 {
		t.Fatalf("Repos length = %d, want 1", len(cfg.Repos))
	}
	if cfg.Repos[0].Name != "test-repo" {
		t.Errorf("Repos[0].Name = %q, want %q",
			cfg.Repos[0].Name, "test-repo")
	}
	if cfg.Repos[0].URL != "https://example.com/recipes" {
		t.Errorf("Repos[0].URL = %q, want %q",
			cfg.Repos[0].URL, "https://example.com/recipes")
	}
}

// BUG-5: repo remove doesn't update config.toml. After
// removing a repo, config.toml should no longer contain it.

func TestRepoRemoveUpdatesConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")

	initial := "[[repos]]\nname = \"test-repo\"\n" +
		"url = \"https://example.com/recipes\"\n"
	if err := os.WriteFile(
		configPath, []byte(initial), 0o644); err != nil {
		t.Fatalf("writing initial config: %v", err)
	}

	err := removeRepoFromConfig(configPath, "test-repo")
	if err != nil {
		t.Fatalf("removeRepoFromConfig error: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}

	cfg, err := config.ParseAppConfig(string(data))
	if err != nil {
		t.Fatalf("parsing config: %v", err)
	}

	if len(cfg.Repos) != 0 {
		t.Errorf("Repos length = %d, want 0", len(cfg.Repos))
	}
}
