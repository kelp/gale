package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAddDryRunDoesNotWriteConfig verifies that `gale add
// --dry-run` does not mutate gale.toml. The dry-run flag is
// the global -n persistent flag; add must honor it.
func TestAddDryRunDoesNotWriteConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	projDir := t.TempDir()
	configPath := filepath.Join(projDir, "gale.toml")
	initial := "[packages]\n"
	if err := os.WriteFile(configPath,
		[]byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	os.Chdir(projDir)
	t.Cleanup(func() { os.Chdir(orig) })

	addProject = true
	dryRun = true
	t.Cleanup(func() {
		addProject = false
		dryRun = false
	})

	// Use @version so the network resolver is not invoked.
	if err := addCmd.RunE(addCmd, []string{"jq@1.8.1"}); err != nil {
		t.Fatalf("add command failed: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != initial {
		t.Errorf("gale.toml mutated under --dry-run:\n%s",
			string(data))
	}
}

// TestAddDryRunDoesNotCreateConfig verifies that `gale add
// --dry-run` does not bootstrap a gale.toml when one does
// not yet exist.
func TestAddDryRunDoesNotCreateConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	projDir := t.TempDir()
	configPath := filepath.Join(projDir, "gale.toml")

	orig, _ := os.Getwd()
	os.Chdir(projDir)
	t.Cleanup(func() { os.Chdir(orig) })

	addGlobal = true
	dryRun = true
	t.Cleanup(func() {
		addGlobal = false
		dryRun = false
	})

	if err := addCmd.RunE(addCmd, []string{"jq@1.8.1"}); err != nil {
		t.Fatalf("add command failed: %v", err)
	}

	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		// In --global mode the path is under $HOME/.gale/
		// — checking the project path is just to verify
		// nothing leaked here. The real check is on the
		// global path below.
		t.Errorf("project gale.toml created under --dry-run")
	}

	globalPath := filepath.Join(home, ".gale", "gale.toml")
	if _, err := os.Stat(globalPath); !os.IsNotExist(err) {
		t.Errorf("global gale.toml created under --dry-run: %s",
			globalPath)
	}
}

// TestAddStripsRevisionFromVersion verifies that when a user
// provides "pkg@version-N" (canonical form with a numeric
// revision suffix), addToConfig writes only the bare version
// (e.g. "1.8.1") to gale.toml, not the full canonical string
// (e.g. "1.8.1-1").
//
// Bug 0017: addToConfig passes the version verbatim to
// config.UpsertPackage, so gale.toml ends up with "1.8.1-1"
// instead of "1.8.1". The fix must strip the revision suffix
// before writing. Non-numeric suffixes (pre-release tags like
// "1.0.0-rc1") must be preserved unchanged.
func TestAddStripsRevisionFromVersion(t *testing.T) {
	tmp := t.TempDir()

	orig, _ := os.Getwd()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(orig) //nolint:errcheck

	configPath := filepath.Join(tmp, "gale.toml")
	// Write a minimal gale.toml so project scope resolves here.
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Save and restore add globals.
	savedGlobal := addGlobal
	savedProject := addProject
	savedHost := addHost
	savedRecipes := addRecipes
	defer func() {
		addGlobal = savedGlobal
		addProject = savedProject
		addHost = savedHost
		addRecipes = savedRecipes
	}()
	addGlobal = false
	addProject = true
	addHost = ""
	addRecipes = ""

	// Call addToConfig directly with a canonical version string
	// that includes a numeric revision suffix.
	// The fix must strip "-1" and write bare "1.8.1".
	if _, err := addToConfig("jq", "1.8.1-1", "", false, true); err != nil {
		t.Fatalf("addToConfig returned error: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading gale.toml: %v", err)
	}
	content := string(data)

	if strings.Contains(content, "1.8.1-1") {
		t.Errorf("gale.toml contains canonical %q — "+
			"add must strip the numeric revision suffix before writing",
			"1.8.1-1")
	}
	if !strings.Contains(content, `"1.8.1"`) {
		t.Errorf("gale.toml content %q does not contain bare version %q — "+
			"revision suffix must be stripped, not the whole version",
			content, "1.8.1")
	}
}

// TestAddPreservesNonNumericPrereleaseTag verifies that a
// pre-release version like "1.0.0-rc1" (where the suffix is
// not a plain integer) is written verbatim to gale.toml.
// Only numeric revision suffixes (Debian-style "-N") must be
// stripped; pre-release tags are part of the version identity.
func TestAddPreservesNonNumericPrereleaseTag(t *testing.T) {
	tmp := t.TempDir()

	orig, _ := os.Getwd()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(orig) //nolint:errcheck

	configPath := filepath.Join(tmp, "gale.toml")
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	savedGlobal := addGlobal
	savedProject := addProject
	savedHost := addHost
	savedRecipes := addRecipes
	defer func() {
		addGlobal = savedGlobal
		addProject = savedProject
		addHost = savedHost
		addRecipes = savedRecipes
	}()
	addGlobal = false
	addProject = true
	addHost = ""
	addRecipes = ""

	// "1.0.0-rc1": the "-rc1" suffix is a pre-release tag, not
	// a numeric revision; it must be preserved.
	if _, err := addToConfig("mytool", "1.0.0-rc1", "", false, true); err != nil {
		t.Fatalf("addToConfig returned error: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading gale.toml: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "1.0.0-rc1") {
		t.Errorf("gale.toml content %q does not preserve pre-release tag %q",
			content, "1.0.0-rc1")
	}
}
