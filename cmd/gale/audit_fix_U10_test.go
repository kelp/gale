package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/config"
)

// TestUpdateNoInstallRevisionBumpWritesVersionedPin
// reproduces issue #66: `gale update --no-install` for a
// revision-only bump (same upstream version, recipe
// revision 1 → 2) used to write the bare version back to
// gale.toml, leaving the pin byte-identical. The promised
// follow-up `gale sync` then resolved the bare pin to the
// already-installed revision and installed nothing. The
// pin must encode the revision so sync sees the drift.
func TestUpdateNoInstallRevisionBumpWritesVersionedPin(t *testing.T) {
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

	// Local recipe at the same upstream version but
	// revision 2 — a security-rebuild style bump.
	recipesDir := filepath.Join(projDir, "recipes", "j")
	if err := os.MkdirAll(recipesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	recipeBody := strings.Join([]string{
		`[package]`,
		`name = "jq"`,
		`version = "1.7.0"`,
		`revision = 2`,
		``,
		`[source]`,
		`url = "https://example.invalid/jq.tar.gz"`,
		`sha256 = "deadbeef"`,
	}, "\n")
	if err := os.WriteFile(
		filepath.Join(recipesDir, "jq.toml"),
		[]byte(recipeBody), 0o644,
	); err != nil {
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

	// The pin must carry the revision: a bare "1.7.0"
	// would be byte-identical to the old pin, and the
	// follow-up sync would resolve it to the on-disk
	// revision 1 and skip revision 2 entirely.
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.ParseGaleConfig(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Packages["jq"] != "1.7.0-2" {
		t.Errorf("gale.toml jq = %q, want %q (revision-only "+
			"bump must write the versioned pin so sync "+
			"detects the drift)",
			cfg.Packages["jq"], "1.7.0-2")
	}
}

// TestUpdateNoInstallVersionBumpKeepsBarePin verifies the
// fix for #66 does not change the normal case: a plain
// version bump at revision 1 still writes the bare
// version, per the gale.toml convention.
func TestUpdateNoInstallVersionBumpKeepsBarePin(t *testing.T) {
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
		[]byte(recipeBody), 0o644,
	); err != nil {
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
	cfg, err := config.ParseGaleConfig(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Packages["jq"] != "1.8.0" {
		t.Errorf("gale.toml jq = %q, want bare %q",
			cfg.Packages["jq"], "1.8.0")
	}
}
