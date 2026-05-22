package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/lockfile"
)

// TestUpdateNoInstallWritesPinSkipsBuild verifies that
// `gale update --no-install jq` updates the version in
// gale.toml but does not invoke the installer. The split
// flow is: `update --no-install` bumps pins, `gale sync`
// installs them — so the user can review the diff before
// touching the store.
func TestUpdateNoInstallWritesPinSkipsBuild(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GALE_OFFLINE", "1")

	projDir := t.TempDir()
	configPath := filepath.Join(projDir, "gale.toml")
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n  jq = \"1.7.0\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	// Local recipe at the new version. The resolver returns
	// this recipe for `jq`, simulating an upstream bump.
	recipesDir := filepath.Join(projDir, "recipes", "j")
	if err := os.MkdirAll(recipesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	recipeBody := strings.Join([]string{
		`[package]`,
		`name = "jq"`,
		`version = "1.8.0"`,
		``,
		`[source]`,
		`url = "https://example.invalid/jq.tar.gz"`,
		`sha256 = "deadbeef"`,
	}, "\n")
	if err := os.WriteFile(
		filepath.Join(recipesDir, "jq.toml"),
		[]byte(recipeBody), 0o644); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	os.Chdir(projDir)
	t.Cleanup(func() { os.Chdir(orig) })

	updateRecipes = filepath.Join(projDir, "recipes")
	updateNoInstall = true
	t.Cleanup(func() {
		updateRecipes = ""
		updateNoInstall = false
	})

	if err := updateCmd.RunE(updateCmd, []string{"jq"}); err != nil {
		t.Fatalf("update --no-install failed: %v", err)
	}

	// gale.toml now lists the new version.
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.ParseGaleConfig(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Packages["jq"] != "1.8.0" {
		t.Errorf("gale.toml jq = %q, want %q",
			cfg.Packages["jq"], "1.8.0")
	}

	// The store must not have a jq install at any version.
	storeJQ := filepath.Join(defaultStoreRoot(), "jq")
	for _, v := range []string{"1.7.0", "1.8.0"} {
		if _, err := os.Stat(filepath.Join(storeJQ, v)); err == nil {
			t.Errorf("store jq@%s exists — update --no-install "+
				"should not build", v)
		}
	}

	// The lockfile should not record the new version. With
	// --no-install, the lock reflects what is installed; the
	// subsequent `gale sync` writes the new hash. An empty or
	// missing lockfile is both acceptable.
	lp := filepath.Join(projDir, "gale.lock")
	if _, statErr := os.Stat(lp); statErr == nil {
		lf, err := lockfile.Read(lp)
		if err != nil {
			t.Fatal(err)
		}
		if entry, ok := lf.Packages["jq"]; ok &&
			entry.Version != "1.7.0" && entry.Version != "" {
			t.Errorf("lockfile jq = %q, want untouched (1.7.0 or absent)",
				entry.Version)
		}
	}
}

// TestUpdateNoInstallSkipsUpToDate verifies that
// --no-install is a no-op when gale.toml already lists
// the latest available version.
func TestUpdateNoInstallSkipsUpToDate(t *testing.T) {
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
		[]byte("[package]\nname = \"jq\"\nversion = \"1.8.0\"\n\n"+
			"[source]\nurl = \"https://example.invalid/jq.tar.gz\"\n"+
			"sha256 = \"deadbeef\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	os.Chdir(projDir)
	t.Cleanup(func() { os.Chdir(orig) })

	updateRecipes = filepath.Join(projDir, "recipes")
	updateNoInstall = true
	t.Cleanup(func() {
		updateRecipes = ""
		updateNoInstall = false
	})

	if err := updateCmd.RunE(updateCmd, []string{"jq"}); err != nil {
		t.Fatalf("update --no-install failed: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Errorf("gale.toml mutated despite up-to-date:\n%s",
			string(data))
	}
}
