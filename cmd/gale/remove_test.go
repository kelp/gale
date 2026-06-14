package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/lockfile"
)

// TestRemoveConfigBeforeStore verifies that the config
// is updated before the store is modified. If the config
// write fails, the store entry must still exist.
func TestRemoveConfigBeforeStore(t *testing.T) {
	// Isolate ~/.gale: the command path registers the project
	// (gh#115) and this test also writes to defaultStoreRoot().
	t.Setenv("HOME", t.TempDir())
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
		filepath.Join(galeDir, "current"),
	); err != nil {
		t.Fatal(err)
	}

	// Create the package in the real store.
	storeRoot := defaultStoreRoot()
	pkgDir := filepath.Join(
		storeRoot, "testpkg", "1.0", "bin",
	)
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
		filepath.Join(storeRoot, "testpkg", "1.0"),
	); statErr != nil {
		t.Error("store entry was deleted despite config " +
			"write failure — operations are in wrong order")
	}
}

// TestRemoveWarnsWhenPackageNotInStore verifies that
// removing a package that is in the config but not in
// the store emits a warning instead of silently no-oping.
func TestRemoveDeletesLockfileEntry(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate ~/.gale (project registry)
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
		filepath.Join(galeDir, "current"),
	); err != nil {
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

// TestRemoveRefusesGale verifies that `gale remove gale`
// is rejected before touching config or store. Removing
// the active binary strands the install with no in-band
// recovery path. The test runs inside an isolated HOME +
// CWD so that a regression (guard removed) can't reach
// the real config.
func TestRemoveRefusesGale(t *testing.T) {
	projDir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(projDir, "gale.toml"),
		[]byte("[packages]\n  gale = \"0.0.0\"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", projDir)

	orig, _ := os.Getwd()
	if err := os.Chdir(projDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })

	removeProject = true
	t.Cleanup(func() { removeProject = false })

	err := removeCmd.RunE(removeCmd, []string{"gale"})
	if err == nil {
		t.Fatal("expected error refusing to remove gale")
	}
	if !strings.Contains(err.Error(), "refusing to remove gale") {
		t.Errorf("expected refusal message, got: %v", err)
	}

	// Guard must refuse *before* touching the config.
	data, readErr := os.ReadFile(
		filepath.Join(projDir, "gale.toml"),
	)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(data), "gale = ") {
		t.Error("guard modified gale.toml; should refuse " +
			"before any write")
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
	// galeConfigDir() resolve under projDir — the command
	// must not touch the real ~/.gale/ during tests.
	t.Setenv("HOME", projDir)

	// Create a generation so rebuild succeeds.
	galeDir := filepath.Join(projDir, ".gale")
	genDir := filepath.Join(galeDir, "gen", "1", "bin")
	if err := os.MkdirAll(genDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Join("gen", "1"),
		filepath.Join(galeDir, "current"),
	); err != nil {
		t.Fatal(err)
	}

	// Empty isolated store; matches the layout
	// defaultStoreRoot() expects under HOME.
	if err := os.MkdirAll(
		filepath.Join(projDir, ".gale", "pkg"), 0o755,
	); err != nil {
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
				"stderr = %q", stderr,
		)
	}
}

// TestRemoveCleansHostOverlayAndShared verifies that when a
// package appears in BOTH shared [packages] and the current
// host's [hosts.<host>.packages] overlay, a single `gale
// remove` clears both. Before the fix, only shared was
// touched: the host overlay entry survived, gale doctor
// then reported the package as "missing" (still in effective
// config, store gone) and offered only `gale sync` —
// reinstalling the thing the user just asked to remove.
func TestRemoveCleansHostOverlayAndShared(t *testing.T) {
	projDir := t.TempDir()
	configPath := filepath.Join(projDir, "gale.toml")
	// Pin the host so the test is deterministic across
	// machines and the section name in the TOML matches what
	// CurrentHost() returns.
	t.Setenv("GALE_HOST", "testhost")
	t.Setenv("HOME", projDir)

	initial := "[packages]\n" +
		"  foo = \"1.0\"\n\n" +
		"[hosts.testhost.packages]\n" +
		"  foo = \"2.0\"\n"
	if err := os.WriteFile(configPath, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	// Pre-create the store entry for the host version —
	// that's the version LoadConfig will surface as
	// "effective" and the one remove will delete from the
	// store.
	storeRoot := filepath.Join(projDir, ".gale", "pkg")
	storePkgDir := filepath.Join(storeRoot, "foo", "2.0", "bin")
	if err := os.MkdirAll(storePkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(storePkgDir, "foo"),
		[]byte("#!/bin/sh\n"), 0o755,
	); err != nil {
		t.Fatal(err)
	}

	// Minimal generation layout so RebuildGeneration finds
	// a previous generation to advance from.
	galeDir := filepath.Join(projDir, ".gale")
	gen1Bin := filepath.Join(galeDir, "gen", "1", "bin")
	if err := os.MkdirAll(gen1Bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Join("gen", "1"),
		filepath.Join(galeDir, "current"),
	); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	os.Chdir(projDir)
	t.Cleanup(func() { os.Chdir(orig) })

	removeProject = true
	t.Cleanup(func() { removeProject = false })

	if err := removeCmd.RunE(removeCmd, []string{"foo"}); err != nil {
		t.Fatalf("remove command failed: %v", err)
	}

	// Both sections must be empty.
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.ParseGaleConfig(string(data))
	if err != nil {
		t.Fatalf("parse config after remove: %v", err)
	}
	if _, has := cfg.Packages["foo"]; has {
		t.Errorf("foo still present in shared [packages]: %q",
			string(data))
	}
	if h, ok := cfg.Hosts["testhost"]; ok {
		if _, has := h.Packages["foo"]; has {
			t.Errorf("foo still present in "+
				"[hosts.testhost.packages]: %q", string(data))
		}
	}

	// Newly built generation must not symlink foo —
	// otherwise PATH points at a deleted store dir.
	currentBin := filepath.Join(galeDir, "current", "bin", "foo")
	if _, err := os.Lstat(currentBin); err == nil {
		t.Errorf(
			"current generation still has bin/foo symlink — " +
				"effective config wasn't fully cleaned before rebuild",
		)
	}

	// Store dir for the removed version must be gone.
	if _, err := os.Stat(
		filepath.Join(storeRoot, "foo", "2.0"),
	); err == nil {
		t.Error("store entry foo@2.0 was not removed")
	}
}
