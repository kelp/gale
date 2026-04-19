package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/lockfile"
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
func TestRemoveDeletesLockfileEntry(t *testing.T) {
	projDir := t.TempDir()
	configPath := filepath.Join(projDir, "gale.toml")
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n  testpkg = \"1.0\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	// Create a lockfile with the package entry.
	lockPath := filepath.Join(projDir, "gale.lock")
	lf := lockfile.LockFile{
		Packages: map[string]lockfile.LockedPackage{
			"testpkg": {Version: "1.0", SHA256: "abc123"},
		},
	}
	if err := lockfile.Write(lockPath, &lf); err != nil {
		t.Fatal(err)
	}

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

	orig, _ := os.Getwd()
	os.Chdir(projDir)
	t.Cleanup(func() { os.Chdir(orig) })

	removeProject = true
	t.Cleanup(func() { removeProject = false })

	if err := removeCmd.RunE(removeCmd, []string{"testpkg"}); err != nil {
		t.Fatalf("remove command failed: %v", err)
	}

	// Verify the lockfile entry is deleted.
	lf2, err := lockfile.Read(lockPath)
	if err != nil {
		t.Fatalf("reading lockfile after remove: %v", err)
	}
	if _, ok := lf2.Packages["testpkg"]; ok {
		t.Error("testpkg should be removed from lockfile")
	}
}

func TestRemoveWarnsWhenPackageNotInStore(t *testing.T) {
	// Project config lists testpkg; an isolated store
	// (rooted under projDir, not the real ~/.gale/pkg)
	// does not contain it; rebuild succeeds against the
	// project generation. The remove command must warn
	// "not found in store" without any interference from
	// whatever happens to live in the user's real store.
	projDir := t.TempDir()
	configPath := filepath.Join(projDir, "gale.toml")
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n  testpkg = \"1.0\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	// Redirect HOME so defaultStoreRoot() and the global
	// galeConfigDir() resolve under projDir. Without this,
	// the remove command's RebuildGeneration runs
	// farm.Repopulate against the user's real ~/.gale/pkg/,
	// which produces dozens of "farm: replacing" stderr
	// lines whenever the user happens to have multiple
	// revisions of the same package on disk — flooding
	// our captured stderr buffer and pushing the actual
	// warning out of view.
	t.Setenv("HOME", projDir)

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

	// Empty isolated store; matches the layout
	// defaultStoreRoot() expects under HOME.
	if err := os.MkdirAll(
		filepath.Join(projDir, ".gale", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	os.Chdir(projDir)
	t.Cleanup(func() { os.Chdir(orig) })

	// Capture stderr via a pipe drained on a goroutine so
	// the writer never blocks regardless of how much output
	// the command produces.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStderr := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = origStderr })

	stderrCh := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(r)
		stderrCh <- string(data)
	}()

	removeProject = true
	t.Cleanup(func() { removeProject = false })

	runErr := removeCmd.RunE(removeCmd, []string{"testpkg"})
	w.Close()
	stderr := <-stderrCh
	os.Stderr = origStderr

	if runErr != nil {
		t.Fatalf("remove command failed: %v", runErr)
	}

	if !strings.Contains(stderr, "not found in store") {
		t.Errorf(
			"expected warning about missing store entry, "+
				"stderr = %q", stderr)
	}
}
