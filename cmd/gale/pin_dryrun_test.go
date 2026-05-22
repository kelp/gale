package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kelp/gale/internal/config"
)

// TestPinDryRunDoesNotWriteConfig verifies that `gale pin
// --dry-run` does not mutate gale.toml. Without this, the
// global -n flag is silently ignored.
func TestPinDryRunDoesNotWriteConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	projDir := t.TempDir()
	configPath := filepath.Join(projDir, "gale.toml")
	initial := "[packages]\n  jq = \"1.8.1\"\n"
	if err := os.WriteFile(configPath,
		[]byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	os.Chdir(projDir)
	t.Cleanup(func() { os.Chdir(orig) })

	dryRun = true
	t.Cleanup(func() { dryRun = false })

	if err := pinCmd.RunE(pinCmd, []string{"jq"}); err != nil {
		t.Fatalf("pin command failed: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.ParseGaleConfig(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Pinned["jq"] {
		t.Error("jq is pinned despite --dry-run")
	}
}

// TestUnpinDryRunDoesNotWriteConfig verifies that `gale unpin
// --dry-run` does not mutate gale.toml.
func TestUnpinDryRunDoesNotWriteConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	projDir := t.TempDir()
	configPath := filepath.Join(projDir, "gale.toml")
	initial := "[packages]\n  jq = \"1.8.1\"\n\n" +
		"[pinned]\n  jq = true\n"
	if err := os.WriteFile(configPath,
		[]byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	os.Chdir(projDir)
	t.Cleanup(func() { os.Chdir(orig) })

	dryRun = true
	t.Cleanup(func() { dryRun = false })

	if err := unpinCmd.RunE(unpinCmd, []string{"jq"}); err != nil {
		t.Fatalf("unpin command failed: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.ParseGaleConfig(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Pinned["jq"] {
		t.Error("jq was unpinned despite --dry-run")
	}
}
