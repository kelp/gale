package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kelp/gale/internal/config"
)

// Tests for the host-union reference guard in `gale remove`
// (Bugbot finding on PR #101). otherScopeReferences must see
// pins under every [hosts.*.packages] section, not just the
// current host's flattened view — otherwise removing one
// host's entry deletes a shared store dir that another
// host's overlay still references.

// TestRemoveHostKeepsStoreWhenOtherHostStillReferences:
// two hosts pin the same package in the global gale.toml.
// `gale remove --host <current>` drops only the current
// host's overlay entry; the store dir must survive because
// the other host's pin still references it. Before the fix,
// the guard flattened the config with ApplyHost(current),
// hiding the foreign-host pin, and deleted the store entry.
func TestRemoveHostKeepsStoreWhenOtherHostStillReferences(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GALE_HOST", "testhost")

	globalDir := filepath.Join(home, ".gale")
	configPath := filepath.Join(globalDir, "gale.toml")
	writeU9File(t, configPath,
		"[hosts.testhost.packages]\n  foo = \"1.0\"\n\n"+
			"[hosts.otherbox.packages]\n  foo = \"1.0\"\n")
	setupU9Generation(t, globalDir)

	// Shared store entry both host overlays reference.
	storeVerDir := filepath.Join(globalDir, "pkg", "foo", "1.0")
	writeU9File(t,
		filepath.Join(storeVerDir, "bin", "foo"), "#!/bin/sh\n")

	orig, _ := os.Getwd()
	if err := os.Chdir(home); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })

	removeGlobal = true
	removeHost = "testhost"
	t.Cleanup(func() {
		removeGlobal = false
		removeHost = ""
	})

	if err := removeCmd.RunE(removeCmd, []string{"foo"}); err != nil {
		t.Fatalf("remove --host testhost failed: %v", err)
	}

	// The current host's overlay entry must be gone.
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.ParseGaleConfig(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if h, ok := cfg.Hosts["testhost"]; ok {
		if _, has := h.Packages["foo"]; has {
			t.Errorf("foo still in [hosts.testhost.packages]: %q",
				string(data))
		}
	}

	// otherbox's pin must survive untouched.
	if h, ok := cfg.Hosts["otherbox"]; !ok {
		t.Errorf("[hosts.otherbox] section gone: %q", string(data))
	} else if _, has := h.Packages["foo"]; !has {
		t.Errorf("foo missing from [hosts.otherbox.packages]: %q",
			string(data))
	}

	// Store entry must survive — otherbox still references it.
	if _, err := os.Stat(storeVerDir); err != nil {
		t.Error("store entry foo@1.0 deleted while " +
			"[hosts.otherbox.packages] still references it")
	}
}

// TestRemoveKeepsStoreWhenForeignHostPinReferences: a
// project-scoped remove must not delete a store dir that the
// global gale.toml pins only under a foreign host's overlay.
// Before the fix, the guard flattened the global config to
// the current host's view, so the host-only pin was
// invisible and the shared store entry was deleted.
func TestRemoveKeepsStoreWhenForeignHostPinReferences(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GALE_HOST", "testhost")

	// Global config references foo@1.0 only for otherbox.
	globalDir := filepath.Join(home, ".gale")
	writeU9File(t, filepath.Join(globalDir, "gale.toml"),
		"[hosts.otherbox.packages]\n  foo = \"1.0\"\n")

	// Project config references the same foo@1.0.
	projDir := filepath.Join(home, "proj")
	writeU9File(t, filepath.Join(projDir, "gale.toml"),
		"[packages]\n  foo = \"1.0\"\n")
	setupU9Generation(t, filepath.Join(projDir, ".gale"))

	// Shared store entry both configs reference.
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

	// Store entry must survive — the global config's
	// otherbox overlay still references it.
	if _, err := os.Stat(storeVerDir); err != nil {
		t.Error("store entry foo@1.0 deleted while the global " +
			"[hosts.otherbox.packages] still references it")
	}
}
