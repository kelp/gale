package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/config"
)

// TestSwitchHasBuildFlag verifies that switchCmd exposes a
// --build flag so users can force a source build when switching
// to a version that has no prebuilt binary in GHCR.
func TestSwitchHasBuildFlag(t *testing.T) {
	if switchCmd.Flags().Lookup("build") == nil {
		t.Error("switchCmd is missing --build flag — " +
			"users must be able to force a source build " +
			"when switching versions")
	}
}

// TestSwitchRejectsMissingPackage verifies that `gale switch
// foo 1.0` refuses to act when foo is not in gale.toml. The
// suggestion in the error points users at `gale install`.
func TestSwitchRejectsMissingPackage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GALE_OFFLINE", "1")

	projDir := t.TempDir()
	configPath := filepath.Join(projDir, "gale.toml")
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n  jq = \"1.8.0\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	os.Chdir(projDir)
	t.Cleanup(func() { os.Chdir(orig) })

	err := switchCmd.RunE(switchCmd, []string{"gh", "2.89.0"})
	if err == nil {
		t.Fatal("expected error for package not in gale.toml")
	}
	if !strings.Contains(err.Error(), "gale install") {
		t.Errorf("error %q should suggest 'gale install'", err.Error())
	}
}

// TestSwitchDryRunDoesNotWriteOrInstall verifies that
// --dry-run shows the action without mutating state.
func TestSwitchDryRunDoesNotWriteOrInstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GALE_OFFLINE", "1")

	projDir := t.TempDir()
	configPath := filepath.Join(projDir, "gale.toml")
	original := "[packages]\n  jq = \"1.8.0\"\n"
	if err := os.WriteFile(configPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	recipesDir := filepath.Join(projDir, "recipes", "j")
	if err := os.MkdirAll(recipesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(recipesDir, "jq.toml"),
		[]byte("[package]\nname = \"jq\"\nversion = \"1.7.0\"\n\n"+
			"[source]\nurl = \"https://example.invalid/jq.tar.gz\"\n"+
			"sha256 = \"deadbeef\"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	os.Chdir(projDir)
	t.Cleanup(func() { os.Chdir(orig) })

	switchRecipes = filepath.Join(projDir, "recipes")
	dryRun = true
	t.Cleanup(func() {
		switchRecipes = ""
		dryRun = false
	})

	if err := switchCmd.RunE(
		switchCmd, []string{"jq", "1.7.0"},
	); err != nil {
		t.Fatalf("switch --dry-run failed: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Errorf("gale.toml mutated under --dry-run:\n%s", string(data))
	}
}

// TestSwitchAcceptsAtVersionForm verifies the @version
// variant: `gale switch jq@1.7.0` is equivalent to
// `gale switch jq 1.7.0`.
func TestSwitchAcceptsAtVersionForm(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GALE_OFFLINE", "1")

	projDir := t.TempDir()
	configPath := filepath.Join(projDir, "gale.toml")
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n  jq = \"1.8.0\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	recipesDir := filepath.Join(projDir, "recipes", "j")
	if err := os.MkdirAll(recipesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(recipesDir, "jq.toml"),
		[]byte("[package]\nname = \"jq\"\nversion = \"1.7.0\"\n\n"+
			"[source]\nurl = \"https://example.invalid/jq.tar.gz\"\n"+
			"sha256 = \"deadbeef\"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	os.Chdir(projDir)
	t.Cleanup(func() { os.Chdir(orig) })

	switchRecipes = filepath.Join(projDir, "recipes")
	dryRun = true
	t.Cleanup(func() {
		switchRecipes = ""
		dryRun = false
	})

	// Single-arg @version form — dry-run skips the install.
	if err := switchCmd.RunE(
		switchCmd, []string{"jq@1.7.0"},
	); err != nil {
		t.Fatalf("switch jq@1.7.0 failed: %v", err)
	}
}

// TestSwitchRequiresVersion verifies that `gale switch jq`
// without a version is rejected.
func TestSwitchRequiresVersion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GALE_OFFLINE", "1")

	projDir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(projDir, "gale.toml"),
		[]byte("[packages]\n  jq = \"1.8.0\"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	os.Chdir(projDir)
	t.Cleanup(func() { os.Chdir(orig) })

	err := switchCmd.RunE(switchCmd, []string{"jq"})
	if err == nil {
		t.Fatal("expected error when version is missing")
	}
}

// TestSwitchWritesPinWhenLocalRecipeMatches verifies the
// success path: when a local recipe matches the requested
// version (so the resolver returns it directly), switch
// updates gale.toml and the install is a cache-hit against
// a prepopulated store entry.
func TestSwitchWritesPinWhenLocalRecipeMatches(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GALE_OFFLINE", "1")

	projDir := t.TempDir()
	configPath := filepath.Join(projDir, "gale.toml")
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n  jq = \"1.8.0\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	recipesDir := filepath.Join(projDir, "recipes", "j")
	if err := os.MkdirAll(recipesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(recipesDir, "jq.toml"),
		[]byte("[package]\nname = \"jq\"\nversion = \"1.7.0\"\n\n"+
			"[source]\nurl = \"https://example.invalid/jq.tar.gz\"\n"+
			"sha256 = \"deadbeef\"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	// Pre-populate the store with jq@1.7.0-1 so Install
	// short-circuits as a cache hit. IsInstalled requires
	// a bin/ subdirectory and finalizeInstall's
	// post-rebuild contract check requires at least one
	// symlinkable file so the package appears in the
	// active generation.
	storeRoot := defaultStoreRoot()
	pkgDir := filepath.Join(storeRoot, "jq", "1.7.0-1", "bin")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(pkgDir, "jq"),
		[]byte("#!/bin/sh\n"), 0o755,
	); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.RemoveAll(filepath.Join(storeRoot, "jq"))
	})

	orig, _ := os.Getwd()
	os.Chdir(projDir)
	t.Cleanup(func() { os.Chdir(orig) })

	switchRecipes = filepath.Join(projDir, "recipes")
	t.Cleanup(func() { switchRecipes = "" })

	if err := switchCmd.RunE(
		switchCmd, []string{"jq", "1.7.0"},
	); err != nil {
		t.Fatalf("switch failed: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.ParseGaleConfig(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Packages["jq"] != "1.7.0" {
		t.Errorf("gale.toml jq = %q, want %q",
			cfg.Packages["jq"], "1.7.0")
	}
}
