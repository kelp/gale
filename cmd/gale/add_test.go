package main

import (
	"os"
	"path/filepath"
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
