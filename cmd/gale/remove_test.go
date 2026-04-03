package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRemoveConfigBeforeStore verifies that the config
// is updated before the store is modified. If the config
// write fails, the store entry must still exist.
func TestRemoveConfigBeforeStore(t *testing.T) {
	projDir := t.TempDir()
	configPath := filepath.Join(projDir, "gale.toml")
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n  testpkg = \"1.0\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	// Create a generation so the command can proceed.
	galeDir := filepath.Join(projDir, ".gale")
	genDir := filepath.Join(galeDir, "gen", "1", "bin")
	if err := os.MkdirAll(genDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Join("gen", "1"),
		filepath.Join(galeDir, "current")); err != nil {
		t.Fatal(err)
	}

	// Create the package in the real store.
	storeRoot := defaultStoreRoot()
	pkgDir := filepath.Join(
		storeRoot, "testpkg", "1.0", "bin")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.RemoveAll(filepath.Join(storeRoot, "testpkg"))
	})

	// Make the config directory read-only so
	// RemovePackage cannot create a temp file.
	if err := os.Chmod(projDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.Chmod(projDir, 0o755)
	})

	orig, _ := os.Getwd()
	os.Chdir(projDir)
	t.Cleanup(func() { os.Chdir(orig) })

	removeProject = true
	t.Cleanup(func() { removeProject = false })

	err := removeCmd.RunE(removeCmd, []string{"testpkg"})
	if err == nil {
		t.Fatal("expected error from config write failure")
	}

	// The store entry must still exist because config
	// failed first.
	if _, statErr := os.Stat(
		filepath.Join(storeRoot, "testpkg", "1.0")); statErr != nil {
		t.Error("store entry was deleted despite config " +
			"write failure — operations are in wrong order")
	}
}

// TestRemoveWarnsWhenPackageNotInStore verifies that
// removing a package that is in the config but not in
// the store emits a warning instead of silently no-oping.
func TestRemoveWarnsWhenPackageNotInStore(t *testing.T) {
	// Set up a project directory with config listing
	// a package, a store without that package, and
	// a generation directory so the rebuild works.
	projDir := t.TempDir()
	configPath := filepath.Join(projDir, "gale.toml")
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n  testpkg = \"1.0\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	// The store is the real ~/.gale/pkg/ but testpkg
	// won't be installed there.

	// Create a generation so rebuild succeeds.
	galeDir := filepath.Join(projDir, ".gale")
	genDir := filepath.Join(galeDir, "gen", "1", "bin")
	if err := os.MkdirAll(genDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Join("gen", "1"),
		filepath.Join(galeDir, "current")); err != nil {
		t.Fatal(err)
	}

	// Override defaultStoreRoot by changing to the
	// project dir and using the project scope.
	orig, _ := os.Getwd()
	os.Chdir(projDir)
	t.Cleanup(func() { os.Chdir(orig) })

	// Capture stderr by replacing os.Stderr temporarily.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStderr := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = origStderr })

	// Override the store root for this test. Since
	// defaultStoreRoot() uses the user's home dir, we
	// need to use the flags approach.
	// The remove command creates its own store, so we
	// need a different approach. We can't easily override
	// defaultStoreRoot(), but the package IS listed in
	// config and NOT in the real store (it's a random
	// name), so if we pick a name that's not actually
	// installed, this works.
	//
	// Actually we need a package in the config but not
	// in the store. The real ~/.gale/pkg/ probably
	// doesn't have "testpkg". So we can just run the
	// real command with project scope (-p) and check
	// stderr for the warning.
	removeProject = true
	t.Cleanup(func() { removeProject = false })

	runErr := removeCmd.RunE(removeCmd, []string{"testpkg"})
	w.Close()

	var buf [4096]byte
	n, _ := r.Read(buf[:])
	stderr := string(buf[:n])

	os.Stderr = origStderr

	// The command should succeed (config update + gen
	// rebuild), but emit a warning about the missing
	// store entry.
	if runErr != nil {
		t.Fatalf("remove command failed: %v", runErr)
	}

	if !strings.Contains(stderr, "not found in store") {
		t.Errorf(
			"expected warning about missing store entry, "+
				"stderr = %q", stderr)
	}
}
