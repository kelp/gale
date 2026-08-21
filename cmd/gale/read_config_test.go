package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReadGaleConfig verifies the happy path: a valid
// gale.toml is read and parsed.
func TestReadGaleConfig(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "gale.toml")
	if err := os.WriteFile(cfgPath,
		[]byte("[packages]\n  jq = \"1.7\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := readGaleConfig(cfgPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v := cfg.Packages["jq"]; v != "1.7" {
		t.Errorf("jq = %q, want %q", v, "1.7")
	}
}

// TestReadGaleConfigIgnoresToolVersions verifies that a
// missing gale.toml with a sibling .tool-versions yields
// an empty package map. Gale reads gale.toml only.
func TestReadGaleConfigIgnoresToolVersions(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "gale.toml")
	tvPath := filepath.Join(tmp, ".tool-versions")
	if err := os.WriteFile(tvPath,
		[]byte("golang 1.26.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := readGaleConfig(cfgPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v := cfg.Packages["go"]; v != "" {
		t.Errorf("go = %q, want empty (ignored .tool-versions)", v)
	}
	if len(cfg.Packages) != 0 {
		t.Errorf("packages = %v, want empty", cfg.Packages)
	}
}

// TestReadGaleConfigEmpty verifies that a missing gale.toml
// returns an empty config.
func TestReadGaleConfigEmpty(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "gale.toml")
	cfg, err := readGaleConfig(cfgPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Packages) != 0 {
		t.Errorf("packages = %v, want empty", cfg.Packages)
	}
}
