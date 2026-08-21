package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/kelp/gale/internal/config"
)

// Tests for the U9 remove-correctness unit (gh#67, gh#74,
// gh#75). Each test isolates HOME so the command can't
// touch the real ~/.gale.

// setupU9Generation creates the minimal gen/1 + current
// symlink layout under galeDir so RebuildGeneration can
// advance from a previous generation.
func setupU9Generation(t *testing.T, galeDir string) {
	t.Helper()
	if err := os.MkdirAll(
		filepath.Join(galeDir, "gen", "1", "bin"), 0o755,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Join("gen", "1"),
		filepath.Join(galeDir, "current"),
	); err != nil {
		t.Fatal(err)
	}
}

// writeU9File writes content to path, creating parent
// directories as needed.
func writeU9File(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestRemoveKeepsStoreWhenGlobalStillReferences captures
// gh#67: a project-scoped remove must not delete the shared
// store dir while the global gale.toml still references it —
// the global generation's symlinks would dangle and binaries
// on the global PATH would stop working without warning.
func TestRemoveKeepsStoreWhenGlobalStillReferences(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GALE_HOST", "testhost")

	// Global config references foo@1.0.
	globalDir := filepath.Join(home, ".gale")
	writeU9File(t, filepath.Join(globalDir, "gale.toml"),
		"[packages]\n  foo = \"1.0\"\n")

	// Project config references the same foo@1.0.
	projDir := filepath.Join(home, "proj")
	writeU9File(t, filepath.Join(projDir, "gale.toml"),
		"[packages]\n  foo = \"1.0\"\n")
	setupU9Generation(t, filepath.Join(projDir, ".gale"))

	// Shared store entry both scopes link against.
	storeVerDir := filepath.Join(globalDir, "pkg", "foo", "1.0")
	writeU9File(t,
		filepath.Join(storeVerDir, "bin", "foo"), "#!/bin/sh\n")

	orig, _ := os.Getwd()
	if err := os.Chdir(projDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })

	removeProject = true
	t.Cleanup(func() { removeProject = false })

	if err := removeCmd.RunE(removeCmd, []string{"foo"}); err != nil {
		t.Fatalf("remove command failed: %v", err)
	}

	// Project config must no longer list foo.
	data, err := os.ReadFile(filepath.Join(projDir, "gale.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.ParseGaleConfig(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if _, has := cfg.Packages["foo"]; has {
		t.Errorf("foo still in project config: %q", string(data))
	}

	// Store entry must survive — the global config still
	// references it.
	if _, err := os.Stat(storeVerDir); err != nil {
		t.Error("store entry foo@1.0 deleted while the global " +
			"gale.toml still references it")
	}
}

// TestRemoveHostFlagRemovesForeignHostEntry captures gh#75:
// `gale remove --host <other-host>` must check membership in
// the targeted host's section, not the current host's
// flattened view — otherwise entries declared for another
// machine can only be removed by hand-editing gale.toml.
func TestRemoveHostFlagRemovesForeignHostEntry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GALE_HOST", "testhost")

	globalDir := filepath.Join(home, ".gale")
	configPath := filepath.Join(globalDir, "gale.toml")
	writeU9File(t, configPath,
		"[hosts.otherbox.packages]\n  foo = \"1.0\"\n")
	setupU9Generation(t, globalDir)

	orig, _ := os.Getwd()
	if err := os.Chdir(home); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })

	removeGlobal = true
	t.Cleanup(func() {
		removeGlobal = false
	})

	if err := removeCmd.RunE(removeCmd, []string{"foo"}); !errors.Is(err, errSwitchHosts) {
		t.Fatalf("remove --host otherbox: %v, want errSwitchHosts", err)
	}
}
