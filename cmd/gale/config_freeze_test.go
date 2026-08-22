package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kelp/gale/internal/registry"
)

// TestNewRegistryIgnoresConfigURL: leftover [registry] url
// must not change the compiled-in index URL.
func TestNewRegistryIgnoresConfigURL(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	galeDir := filepath.Join(home, ".gale")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(galeDir, "config.toml"),
		[]byte("[registry]\nurl = \"https://example.invalid/recipes\"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	reg, err := newRegistry()
	if err != nil {
		t.Fatalf("newRegistry: %v", err)
	}
	if reg.BaseURL != registry.DefaultURL {
		t.Errorf("BaseURL = %q, want compiled-in %q",
			reg.BaseURL, registry.DefaultURL)
	}
}

// TestNewCmdContextIgnoresJobsKnobs: leftover GALE_JOBS and
// [sync] parallelism must not fail context construction.
func TestNewCmdContextIgnoresJobsKnobs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GALE_JOBS", "3")
	if err := os.MkdirAll(filepath.Join(home, ".gale"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(home, ".gale", "config.toml"),
		[]byte("[sync]\nparallelism = 1\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	tmp := t.TempDir()
	if err := os.WriteFile(
		tmp+"/gale.toml", []byte("[packages]\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(origDir) })

	if _, err := newCmdContext("", false, false); err != nil {
		t.Fatalf("newCmdContext: %v", err)
	}
}
