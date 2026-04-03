package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kelp/gale/internal/config"
)

func TestPinPackage_RejectsRemovedPackage(t *testing.T) {
	// Create a gale.toml with only "jq" installed.
	dir := t.TempDir()
	configPath := filepath.Join(dir, "gale.toml")
	if err := os.WriteFile(configPath,
		[]byte("[packages]\njq = \"1.8.1\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	// Pinning a package not in [packages] should fail.
	err := config.PinPackage(configPath, "ripgrep")
	if err == nil {
		t.Fatal("expected error when pinning absent package")
	}
}

func TestPinPackage_AcceptsInstalledPackage(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "gale.toml")
	if err := os.WriteFile(configPath,
		[]byte("[packages]\njq = \"1.8.1\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	err := config.PinPackage(configPath, "jq")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify jq is pinned.
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.ParseGaleConfig(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Pinned["jq"] {
		t.Error("expected jq to be pinned")
	}
}
