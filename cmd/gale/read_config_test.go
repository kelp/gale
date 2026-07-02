package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReadConfigOrToolVersionsGaleToml verifies the happy path:
// a valid gale.toml is read and parsed.
func TestReadConfigOrToolVersionsGaleToml(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "gale.toml")
	if err := os.WriteFile(cfgPath,
		[]byte("[packages]\n  jq = \"1.7\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := readConfigOrToolVersions(cfgPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v := cfg.Packages["jq"]; v != "1.7" {
		t.Errorf("jq = %q, want %q", v, "1.7")
	}
}

// TestReadConfigOrToolVersionsFallback verifies the fallback:
// when gale.toml is absent, .tool-versions is read.
func TestReadConfigOrToolVersionsFallback(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "gale.toml")
	tvPath := filepath.Join(tmp, ".tool-versions")
	if err := os.WriteFile(tvPath,
		[]byte("golang 1.26.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := readConfigOrToolVersions(cfgPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v := cfg.Packages["go"]; v != "1.26.1" {
		t.Errorf("go = %q, want %q", v, "1.26.1")
	}
}

// TestReadConfigOrToolVersionsEmpty verifies that absent both
// gale.toml and .tool-versions returns an empty config.
func TestReadConfigOrToolVersionsEmpty(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "gale.toml")
	cfg, err := readConfigOrToolVersions(cfgPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Packages) != 0 {
		t.Errorf("packages = %v, want empty", cfg.Packages)
	}
}
