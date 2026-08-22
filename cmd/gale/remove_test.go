package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/lockgraph"
)

// TestRemoveConfigBeforeStore verifies that the config
// is updated before the store is modified. If the config
// write fails, the store entry must still exist.
func TestRemoveConfigBeforeStore(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: the read-only config dir is still writable")
	}
	// Isolate ~/.gale: a project remove that rebuilds
	// registers the project (gh#115) and this test also
	// writes to defaultStoreRoot().
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

// TestRemoveRegeneratesLockTarget replaces the entry-deleting
// contract this test pinned under the flat schema. An enforced lock
// records the whole closure, so deleting one entry would leave the
// removed package's dependencies rooted by nothing and its identity
// still in the target. The target is rebuilt instead: the surviving
// root stays, a dependency it shares with the removed package
// survives, and the removed package's own subgraph goes with it.
func TestRemoveRegeneratesLockTarget(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate ~/.gale (project registry)
	projDir := t.TempDir()
	configPath := filepath.Join(projDir, "gale.toml")
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n  keep = \"1.0.0\"\n  drop = \"2.0.0\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	lockPath := filepath.Join(projDir, "gale.lock")
	platform := currentPlatform()
	art := func(deps ...string) lockfile.Artifact {
		return lockfile.Artifact{
			SHA256:      testSHA,
			Method:      lockgraph.MethodBinary,
			RuntimeDeps: deps,
			GraphDigest: "sha256:" + testSHA,
		}
	}
	node := func(deps ...string) lockfile.Package {
		return lockfile.Package{
			Artifacts: map[string]lockfile.Artifact{platform: art(deps...)},
		}
	}
	if err := lockfile.WriteV1(lockPath, &lockfile.V1{
		Version: lockfile.SchemaVersion,
		Targets: lockfile.Targets{
			Default: &lockfile.Target{
				Roots: []string{"drop@2.0.0-1", "keep@1.0.0-1"},
			},
		},
		Packages: map[string]lockfile.Package{
			"keep@1.0.0-1":   node("shared@3.0.0-1"),
			"drop@2.0.0-1":   node("shared@3.0.0-1", "solo@4.0.0-1"),
			"shared@3.0.0-1": node(),
			"solo@4.0.0-1":   node(),
		},
	}); err != nil {
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

	if err := removeCmd.RunE(removeCmd, []string{"drop"}); !errors.Is(err, errSwitchV1) {
		t.Fatalf("remove v1 lock: %v, want errSwitchV1", err)
	}
}

// TestRemoveDropsTheTargetOfAnEmptiedSection: removing a section's
// last package leaves no section to describe, so its target goes
// rather than being written empty. An empty target would claim the
// section declares nothing to lock, which is a different statement
// from the section being gone, and every other target must survive
// untouched.
func TestRemoveDropsTheTargetOfAnEmptiedSection(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GALE_HOST", "thisbox")
	projDir := t.TempDir()
	configPath := filepath.Join(projDir, "gale.toml")
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n  drop = \"2.0.0\"\n\n"+
			"[hosts.otherbox.packages]\n  keep = \"1.0.0\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	lockPath := filepath.Join(projDir, "gale.lock")
	node := func() lockfile.Package {
		return lockfile.Package{
			Artifacts: map[string]lockfile.Artifact{
				currentPlatform(): {
					SHA256:      testSHA,
					Method:      lockgraph.MethodBinary,
					GraphDigest: "sha256:" + testSHA,
				},
			},
		}
	}
	if err := lockfile.WriteV1(lockPath, &lockfile.V1{
		Version: lockfile.SchemaVersion,
		Targets: lockfile.Targets{
			Default: &lockfile.Target{Roots: []string{"drop@2.0.0-1"}},
			Host: map[string]lockfile.Target{
				"otherbox": {Roots: []string{"keep@1.0.0-1"}},
			},
		},
		Packages: map[string]lockfile.Package{
			"drop@2.0.0-1": node(),
			"keep@1.0.0-1": node(),
		},
	}); err != nil {
		t.Fatal(err)
	}

	galeDir := filepath.Join(projDir, ".gale")
	if err := os.MkdirAll(filepath.Join(galeDir, "gen", "1", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Join("gen", "1"), filepath.Join(galeDir, "current"),
	); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	os.Chdir(projDir)
	t.Cleanup(func() { os.Chdir(orig) })

	removeProject = true
	t.Cleanup(func() { removeProject = false })

	if err := removeCmd.RunE(removeCmd, []string{"drop"}); !errors.Is(err, errSwitchHosts) {
		t.Fatalf("remove host overlay: %v, want errSwitchHosts", err)
	}
}

// TestRemoveInAnUnlockedProjectWritesNoLock: a project with no
// gale.lock is in unlocked mode, and removing a package must not
// conjure one. The removal verifies nothing, so there is nothing to
// carry and nothing to root; absent has to stay absent rather than
// becoming an error or an invented lock.
func TestRemoveInAnUnlockedProjectWritesNoLock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	projDir := t.TempDir()
	configPath := filepath.Join(projDir, "gale.toml")
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n  a = \"1.0\"\n  b = \"2.0\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	galeDir := filepath.Join(projDir, ".gale")
	if err := os.MkdirAll(filepath.Join(galeDir, "gen", "1", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Join("gen", "1"), filepath.Join(galeDir, "current"),
	); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	os.Chdir(projDir)
	t.Cleanup(func() { os.Chdir(orig) })

	removeProject = true
	t.Cleanup(func() { removeProject = false })

	var runErr error
	stderr := captureStderr(t, func() {
		runErr = removeCmd.RunE(removeCmd, []string{"a"})
	})
	if runErr != nil {
		t.Fatalf("remove in an unlocked project failed: %v", runErr)
	}
	// Nothing is stale against a lock that does not exist, so the
	// surviving declaration gets no remedy printed at it. Unlocked
	// mode is a supported state with its own warning, not a lock to
	// repair.
	if strings.Contains(stderr, "not locked") {
		t.Errorf("remove named an unlocked package in a project with no lock:\n%s", stderr)
	}

	if _, err := os.Lstat(filepath.Join(projDir, "gale.lock")); !os.IsNotExist(err) {
		data, _ := os.ReadFile(filepath.Join(projDir, "gale.lock"))
		t.Errorf("remove created a lockfile:\n%s", data)
	}
	cfg, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cfg), "a = ") {
		t.Errorf("gale.toml still declares a:\n%s", cfg)
	}
}

// TestRemoveNamesWhatItCouldNotLock: when the write leaves a
// declared package with no root, §11 requires the writer to name it
// so the caller can print the remedy. The lock is then stale against
// gale.toml, which is recoverable and has to be visible — a silently
// stale lock is the state the next sync fails on with no explanation
// of how it got there.
func TestRemoveNamesWhatItCouldNotLock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	projDir := t.TempDir()
	configPath := filepath.Join(projDir, "gale.toml")
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n  drop = \"2.0.0\"\n  orphan = \"3.0.0\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	// The lock roots only the package being removed, so the survivor
	// has no prior subgraph to carry.
	lockPath := filepath.Join(projDir, "gale.lock")
	if err := lockfile.WriteV1(lockPath, &lockfile.V1{
		Version: lockfile.SchemaVersion,
		Targets: lockfile.Targets{
			Default: &lockfile.Target{Roots: []string{"drop@2.0.0-1"}},
		},
		Packages: map[string]lockfile.Package{
			"drop@2.0.0-1": {Artifacts: map[string]lockfile.Artifact{
				currentPlatform(): {
					SHA256:      testSHA,
					Method:      lockgraph.MethodBinary,
					GraphDigest: "sha256:" + testSHA,
				},
			}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	galeDir := filepath.Join(projDir, ".gale")
	if err := os.MkdirAll(filepath.Join(galeDir, "gen", "1", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Join("gen", "1"), filepath.Join(galeDir, "current"),
	); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	os.Chdir(projDir)
	t.Cleanup(func() { os.Chdir(orig) })

	removeProject = true
	t.Cleanup(func() { removeProject = false })

	var runErr error
	_ = captureStderr(t, func() {
		runErr = removeCmd.RunE(removeCmd, []string{"drop"})
	})
	if !errors.Is(runErr, errSwitchV1) {
		t.Fatalf("remove v1 lock: %v, want errSwitchV1", runErr)
	}
}

// TestRemoveRemedyNamesTheHostSection: the remedy printed for an
// unlocked package has to restore the section it was declared in.
// Plain `gale install` writes shared [packages] unless this machine's
// exact overlay already lists the package, so for a host overlay the
// only command that puts the root back where it belongs carries
// --host. One line per package, too: `gale install` takes exactly one
// package name, so a single line naming several is not runnable.
func TestRemoveRemedyNamesTheHostSection(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GALE_HOST", "thisbox")
	projDir := t.TempDir()
	configPath := filepath.Join(projDir, "gale.toml")
	if err := os.WriteFile(configPath,
		[]byte("[hosts.otherbox.packages]\n  drop = \"2.0.0\"\n"+
			"  orphan = \"3.0.0\"\n  second = \"4.0.0\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	lockPath := filepath.Join(projDir, "gale.lock")
	if err := lockfile.WriteV1(lockPath, &lockfile.V1{
		Version: lockfile.SchemaVersion,
		Targets: lockfile.Targets{
			Host: map[string]lockfile.Target{
				"otherbox": {Roots: []string{"drop@2.0.0-1"}},
			},
		},
		Packages: map[string]lockfile.Package{
			"drop@2.0.0-1": {Artifacts: map[string]lockfile.Artifact{
				currentPlatform(): {
					SHA256:      testSHA,
					Method:      lockgraph.MethodBinary,
					GraphDigest: "sha256:" + testSHA,
				},
			}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	galeDir := filepath.Join(projDir, ".gale")
	if err := os.MkdirAll(filepath.Join(galeDir, "gen", "1", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Join("gen", "1"), filepath.Join(galeDir, "current"),
	); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	os.Chdir(projDir)
	t.Cleanup(func() { os.Chdir(orig) })

	removeProject = true
	t.Cleanup(func() {
		removeProject = false
	})

	var runErr error
	_ = captureStderr(t, func() {
		runErr = removeCmd.RunE(removeCmd, []string{"drop"})
	})
	if !errors.Is(runErr, errSwitchHosts) {
		t.Fatalf("remove host overlay: %v, want errSwitchHosts", runErr)
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

	chdirTo(t, projDir)

	removeProject = true
	t.Cleanup(func() { removeProject = false })

	var runErr error
	stderr := captureStderr(t, func() {
		runErr = removeCmd.RunE(removeCmd, []string{"testpkg"})
	})

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

	if err := removeCmd.RunE(removeCmd, []string{"foo"}); !errors.Is(err, errSwitchHosts) {
		t.Fatalf("remove host overlay: %v, want errSwitchHosts", err)
	}
}
