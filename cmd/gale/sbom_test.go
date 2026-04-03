package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveSbomConfig_FallsBackToGlobal(t *testing.T) {
	// Create a temp dir with no gale.toml (simulates a
	// global-only user's cwd).
	noProject := t.TempDir()

	// Create a fake global config dir with gale.toml.
	globalDir := t.TempDir()
	globalConfig := filepath.Join(globalDir, "gale.toml")
	if err := os.WriteFile(globalConfig,
		[]byte("[packages]\njq = \"1.8.1\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	data, path, err := resolveSbomConfig(noProject, globalDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != globalConfig {
		t.Errorf("path = %q, want %q", path, globalConfig)
	}
	if len(data) == 0 {
		t.Error("expected non-empty config data")
	}
}

func TestResolveSbomConfig_PrefersProject(t *testing.T) {
	// Create a temp dir with a project gale.toml.
	projDir := t.TempDir()
	projConfig := filepath.Join(projDir, "gale.toml")
	if err := os.WriteFile(projConfig,
		[]byte("[packages]\nripgrep = \"14.0\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	globalDir := t.TempDir()

	data, path, err := resolveSbomConfig(projDir, globalDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != projConfig {
		t.Errorf("path = %q, want %q", path, projConfig)
	}
	if len(data) == 0 {
		t.Error("expected non-empty config data")
	}
}
