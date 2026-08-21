package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
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
	t.Cleanup(func() {
		removeGlobal = false
	})

	if err := removeCmd.RunE(removeCmd, []string{"foo"}); !errors.Is(err, errSwitchHosts) {
		t.Fatalf("remove --host testhost: %v, want errSwitchHosts", err)
	}
}

// TestRemoveKeepsStoreWhenForeignHostPinReferences: a
// project-scoped remove must not delete a store dir that the
// global gale.toml pins only under a foreign host's overlay.
// Before the fix, the guard flattened the global config to
// the current host's view, so the host-only pin was
// invisible and the shared store entry was deleted.
func TestRemoveKeepsStoreWhenForeignHostPinReferences(t *testing.T) {
	runU9ProjectRemoveKeepsStore(t,
		"[hosts.otherbox.packages]\n  foo = \"1.0\"\n",
		"store entry foo@1.0 deleted while the global "+
			"[hosts.otherbox.packages] still references it")
}
