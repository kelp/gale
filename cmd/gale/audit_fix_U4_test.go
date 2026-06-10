package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/depsmeta"
	"github.com/kelp/gale/internal/filelock"
	"github.com/kelp/gale/internal/store"
)

// setupGCHome isolates HOME (and the registry, via
// GALE_OFFLINE) for an end-to-end gcCmd.RunE test, chdirs to a
// directory with no project gale.toml, and returns the global
// gale dir and store root that gc will operate on.
func setupGCHome(t *testing.T) (galeDir, storeRoot string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GALE_OFFLINE", "1")
	galeDir = filepath.Join(home, ".gale")
	storeRoot = filepath.Join(galeDir, "pkg")
	if err := os.MkdirAll(storeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(home); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
	dryRun = false
	t.Cleanup(func() { dryRun = false })
	return galeDir, storeRoot
}

// writeGlobalConfig writes ~/.gale/gale.toml.
func writeGlobalConfig(t *testing.T, galeDir, content string) {
	t.Helper()
	if err := os.WriteFile(
		filepath.Join(galeDir, "gale.toml"), []byte(content), 0o644,
	); err != nil {
		t.Fatal(err)
	}
}

// mkStorePkg creates a populated store dir for name@version
// and returns its path.
func mkStorePkg(t *testing.T, storeRoot, name, version string) string {
	t.Helper()
	dir := filepath.Join(storeRoot, name, version)
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(dir, "bin", name), []byte("#!/bin/sh\n"), 0o755,
	); err != nil {
		t.Fatal(err)
	}
	return dir
}

// mkActiveGen creates gen/<n> with a symlink per target binary
// and points current at gen/<n>.
func mkActiveGen(t *testing.T, galeDir string, n int, binTargets ...string) {
	t.Helper()
	binDir := filepath.Join(galeDir, "gen", strconv.Itoa(n), "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, target := range binTargets {
		if err := os.Symlink(
			target, filepath.Join(binDir, filepath.Base(target)),
		); err != nil {
			t.Fatal(err)
		}
	}
	currentPath := filepath.Join(galeDir, "current")
	_ = os.Remove(currentPath)
	if err := os.Symlink(
		filepath.Join("gen", strconv.Itoa(n)), currentPath,
	); err != nil {
		t.Fatal(err)
	}
}

// ageEntry pushes a file/dir mtime back past the gc sweep
// grace period.
func ageEntry(t *testing.T, path string) {
	t.Helper()
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

// TestGCRetainsActiveGenerationLinks pins gh#46: a store
// version linked by the ACTIVE generation must survive gc even
// when the config pins a different (not-installed) version.
// Pre-fix, gc built its retention set from configs only,
// deleted jq/1.7-1, and left current/bin/jq dangling.
func TestGCRetainsActiveGenerationLinks(t *testing.T) {
	galeDir, storeRoot := setupGCHome(t)
	// Config pins jq@1.8, which was never installed.
	writeGlobalConfig(t, galeDir, "[packages]\njq = \"1.8\"\n")
	// The store holds jq/1.7-1, and the active gen links it.
	jqDir := mkStorePkg(t, storeRoot, "jq", "1.7-1")
	mkActiveGen(t, galeDir, 1, filepath.Join(jqDir, "bin", "jq"))

	_ = gcCmd.RunE(gcCmd, nil)

	if _, err := os.Stat(jqDir); err != nil {
		t.Errorf("jq/1.7-1 is linked by the active generation "+
			"and must survive gc: %v", err)
	}
	// The active gen's bin entry must still resolve.
	if _, err := os.Stat(
		filepath.Join(galeDir, "current", "bin", "jq"),
	); err != nil {
		t.Errorf("current/bin/jq dangles after gc: %v", err)
	}
}

// TestGCPreservesRollbackGeneration pins the rollback-discard
// half of gh#46/gh#47: after `gale generations rollback`,
// current points at an older gen. gc must not advance current
// back to config state (pre-fix it rebuilt unconditionally
// after deleting, silently discarding the rollback) and must
// not destroy the generation history that rollback relies on.
func TestGCPreservesRollbackGeneration(t *testing.T) {
	galeDir, storeRoot := setupGCHome(t)
	writeGlobalConfig(t, galeDir, "[packages]\njq = \"1.7\"\n")
	jqDir := mkStorePkg(t, storeRoot, "jq", "1.7-1")
	// Something for gc to actually remove, so the post-removal
	// path runs.
	mkStorePkg(t, storeRoot, "old", "1.0-1")

	// gen/1 and gen/2 both link jq; user rolled back to gen/1.
	mkActiveGen(t, galeDir, 2, filepath.Join(jqDir, "bin", "jq"))
	mkActiveGen(t, galeDir, 1, filepath.Join(jqDir, "bin", "jq"))

	_ = gcCmd.RunE(gcCmd, nil)

	target, err := os.Readlink(filepath.Join(galeDir, "current"))
	if err != nil {
		t.Fatalf("read current symlink: %v", err)
	}
	if filepath.Base(target) != "1" {
		t.Errorf("gc moved current to gen/%s — a rollback to "+
			"gen/1 must survive gc", filepath.Base(target))
	}
}

// TestGCRetainsDepsFromInstalledMetadata pins gh#48: retention
// for runtime deps must derive from the dependent's installed
// .gale-deps.toml, not the registry's current recipe version.
// Here the resolver is unavailable (offline, cold cache) —
// pre-fix, dep expansion was silently skipped and the live
// openssl store dir was reaped out from under curl's rpath.
func TestGCRetainsDepsFromInstalledMetadata(t *testing.T) {
	galeDir, storeRoot := setupGCHome(t)
	writeGlobalConfig(t, galeDir, "[packages]\ncurl = \"8.0.0\"\n")
	curlDir := mkStorePkg(t, storeRoot, "curl", "8.0.0-1")
	osslDir := mkStorePkg(t, storeRoot, "openssl", "3.5.0-1")
	// curl's installed metadata records the exact dep closure
	// it was built against.
	if err := depsmeta.Write(curlDir, depsmeta.Metadata{
		Deps: []depsmeta.ResolvedDep{
			{Name: "openssl", Version: "3.5.0", Revision: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}

	_ = gcCmd.RunE(gcCmd, nil)

	if _, err := os.Stat(osslDir); err != nil {
		t.Errorf("openssl/3.5.0-1 is recorded in curl's "+
			".gale-deps.toml and must survive gc: %v", err)
	}
}

// TestGCRetainsOtherHostPins pins the host-union retention gap
// (gh#48 follow-on): the store is shared across hosts via a
// synced config, so a pin under another host's
// [hosts.*.packages] overlay must keep its store entry alive
// even though ApplyHost hides it on this machine.
func TestGCRetainsOtherHostPins(t *testing.T) {
	galeDir, storeRoot := setupGCHome(t)
	other := config.CurrentHost() + "-other"
	writeGlobalConfig(t, galeDir,
		"[packages]\n[hosts."+other+".packages]\njq = \"1.7\"\n")
	jqDir := mkStorePkg(t, storeRoot, "jq", "1.7-1")

	_ = gcCmd.RunE(gcCmd, nil)

	if _, err := os.Stat(jqDir); err != nil {
		t.Errorf("jq/1.7-1 is pinned by another host's overlay "+
			"and must survive gc: %v", err)
	}
}

// TestGCAbortsWhenProjectScopeUnresolvable pins the PR#104
// review finding: when a project config exists but
// galeDirForConfig fails, gc must abort instead of silently
// dropping the whole project scope (config pins, active project
// generation, swap-link sweep) from its retention set — gc is
// destructive, and a shrunken retained set deletes store
// versions the project still references. On Linux the only
// failure mode of galeDirForConfig is os.UserHomeDir with $HOME
// empty, which is injected after fixture setup; pre-fix, gc
// treated the error like the config-is-global case and returned
// nil.
func TestGCAbortsWhenProjectScopeUnresolvable(t *testing.T) {
	galeDir, storeRoot := setupGCHome(t)
	writeGlobalConfig(t, galeDir, "[packages]\n")
	jqDir := mkStorePkg(t, storeRoot, "jq", "1.7-1")

	// A project config FindGaleConfig will discover from cwd
	// (the walk needs no $HOME).
	projDir := filepath.Join(filepath.Dir(galeDir), "project")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(projDir, "gale.toml"),
		[]byte("[packages]\njq = \"1.7\"\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(projDir); err != nil {
		t.Fatal(err)
	}

	// Injection: blank $HOME so galeDirForConfig's
	// os.UserHomeDir call fails inside RunE.
	t.Setenv("HOME", "")

	if err := gcCmd.RunE(gcCmd, nil); err == nil {
		t.Error("gc must abort when the project gale dir " +
			"cannot be resolved — proceeding shrinks the " +
			"retention set")
	}
	if _, err := os.Stat(jqDir); err != nil {
		t.Errorf("store entry must survive an aborted gc: %v", err)
	}
}

// TestGCSweepsCrashLeftovers pins gh#78: transient store
// entries stranded by a killed install (.build-*, *.bak,
// *.stream) and stale current-new.<pid> swap symlinks must be
// reclaimed by gc once they are older than the sweep grace
// period. Pre-fix, store.List skipped them and gc reported
// "Nothing to clean up." forever.
func TestGCSweepsCrashLeftovers(t *testing.T) {
	galeDir, storeRoot := setupGCHome(t)
	writeGlobalConfig(t, galeDir, "[packages]\njq = \"1.8.1\"\n")
	jqDir := mkStorePkg(t, storeRoot, "jq", "1.8.1-1")

	leftovers := []string{
		filepath.Join(storeRoot, "jq", ".build-abc123"),
		filepath.Join(storeRoot, "jq", "1.8.1-1.bak"),
		filepath.Join(storeRoot, "jq", "1.8.1-1.stream"),
		filepath.Join(storeRoot, "go", ".build-xyz"),
	}
	for _, dir := range leftovers {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(dir, "file"), []byte("x"), 0o644,
		); err != nil {
			t.Fatal(err)
		}
		ageEntry(t, dir)
	}
	// Stale swap symlink from a crashed generation swap.
	staleLink := filepath.Join(galeDir, "current-new.99999")
	if err := os.Symlink("/nonexistent", staleLink); err != nil {
		t.Fatal(err)
	}
	old := unix.NsecToTimeval(
		time.Now().Add(-2 * time.Hour).UnixNano(),
	)
	if err := unix.Lutimes(
		staleLink, []unix.Timeval{old, old},
	); err != nil {
		t.Fatal(err)
	}

	_ = gcCmd.RunE(gcCmd, nil)

	for _, dir := range leftovers {
		if _, err := os.Lstat(dir); !os.IsNotExist(err) {
			t.Errorf("crash leftover %s must be swept by gc", dir)
		}
	}
	if _, err := os.Lstat(staleLink); !os.IsNotExist(err) {
		t.Error("stale current-new.99999 swap symlink must be " +
			"swept by gc")
	}
	if _, err := os.Stat(jqDir); err != nil {
		t.Errorf("referenced jq/1.8.1-1 must survive the "+
			"transient sweep: %v", err)
	}
}

// TestGCSweepsBuildScratch pins gh#79: ~/.gale/tmp build
// scratch stranded by interrupted builds must be swept by gc
// once past the grace period, while fresh entries (possibly an
// active build) are left alone.
func TestGCSweepsBuildScratch(t *testing.T) {
	galeDir, _ := setupGCHome(t)
	writeGlobalConfig(t, galeDir, "[packages]\n")
	tmpDir := filepath.Join(galeDir, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		t.Fatal(err)
	}

	stale := []string{
		filepath.Join(tmpDir, "gale-build-leak"),
		filepath.Join(tmpDir, "gale-tools-leak"),
		filepath.Join(tmpDir, "gale-home-leak"),
		filepath.Join(tmpDir, "gale-tmp-leak"),
		filepath.Join(tmpDir, "gale-install-leak"),
		filepath.Join(tmpDir, "gale-git-leak"),
	}
	for _, dir := range stale {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		ageEntry(t, dir)
	}
	fresh := filepath.Join(tmpDir, "gale-build-active")
	if err := os.MkdirAll(fresh, 0o755); err != nil {
		t.Fatal(err)
	}

	_ = gcCmd.RunE(gcCmd, nil)

	for _, dir := range stale {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("stale build scratch %s must be swept by gc",
				dir)
		}
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh scratch (possible active build) must "+
			"survive gc: %v", err)
	}
}

// TestSweepTransientSkipsHeldLock guards the gh#78 sweep
// against a concurrent install: a name dir whose lock file is
// flock-held may have live staging dirs of any age, so the
// sweep must leave the whole name dir alone.
func TestSweepTransientSkipsHeldLock(t *testing.T) {
	storeRoot := t.TempDir()
	buildDir := filepath.Join(storeRoot, "jq", ".build-active")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ageEntry(t, buildDir)

	// Hold the per-package lock like an in-flight install does.
	unlock, err := filelock.Acquire(
		filepath.Join(storeRoot, "jq", "1.8.1-1.lock"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	s := store.NewStore(storeRoot)
	swept := s.SweepTransient(time.Hour, false)
	if len(swept) != 0 {
		t.Errorf("sweep must skip a name dir with a held "+
			"lock, swept: %v", swept)
	}
	if _, err := os.Stat(buildDir); err != nil {
		t.Errorf(".build-active must survive while the package "+
			"lock is held: %v", err)
	}

	// Once the lock is released the same entry is reclaimable.
	unlock()
	swept = s.SweepTransient(time.Hour, false)
	if len(swept) != 1 {
		t.Errorf("after unlock, want 1 swept entry, got %v", swept)
	}
}

// TestSweepTransientAgeGuard verifies the gh#78 age guard: a
// transient entry younger than maxAge is never swept, even
// with no lock held — it may belong to an install that has not
// taken its lock yet or crashed moments ago.
func TestSweepTransientAgeGuard(t *testing.T) {
	storeRoot := t.TempDir()
	freshDir := filepath.Join(storeRoot, "jq", ".build-fresh")
	if err := os.MkdirAll(freshDir, 0o755); err != nil {
		t.Fatal(err)
	}

	s := store.NewStore(storeRoot)
	if swept := s.SweepTransient(time.Hour, false); len(swept) != 0 {
		t.Errorf("fresh transient entry must not be swept: %v",
			swept)
	}
	if _, err := os.Stat(freshDir); err != nil {
		t.Errorf(".build-fresh must survive the sweep: %v", err)
	}
}

// TestGCBuildScratchVetoedWhileInstallInFlight guards the
// gh#79 sweep: scratch under ~/.gale/tmp cannot be attributed
// to a package, so while ANY per-package store lock is held
// the whole tmp sweep must be skipped.
func TestGCBuildScratchVetoedWhileInstallInFlight(t *testing.T) {
	galeDir, storeRoot := setupGCHome(t)
	writeGlobalConfig(t, galeDir, "[packages]\n")
	tmpDir := filepath.Join(galeDir, "tmp")
	stale := filepath.Join(tmpDir, "gale-build-leak")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	ageEntry(t, stale)

	// Simulate an in-flight install elsewhere in the store.
	if err := os.MkdirAll(
		filepath.Join(storeRoot, "go"), 0o755,
	); err != nil {
		t.Fatal(err)
	}
	unlock, err := filelock.Acquire(
		filepath.Join(storeRoot, "go", "1.22.0-1.lock"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	_ = gcCmd.RunE(gcCmd, nil)

	if _, err := os.Stat(stale); err != nil {
		t.Errorf("tmp sweep must be vetoed while an install "+
			"holds a store lock: %v", err)
	}
}

// TestGCFromGlobalGaleDirNoNestedGaleDir pins the gh#96 site in
// gc: running gc from inside ~/.gale/ made FindGaleConfig treat
// the global gale.toml as a project config, and the project
// gale dir was derived as ~/.gale/.gale — pre-fix the rebuild
// then materialized that bogus directory.
func TestGCFromGlobalGaleDirNoNestedGaleDir(t *testing.T) {
	galeDir, storeRoot := setupGCHome(t)
	writeGlobalConfig(t, galeDir, "[packages]\njq = \"1.7\"\n")
	jqDir := mkStorePkg(t, storeRoot, "jq", "1.7-1")
	mkStorePkg(t, storeRoot, "old", "1.0-1") // something to remove
	mkActiveGen(t, galeDir, 1, filepath.Join(jqDir, "bin", "jq"))
	if err := os.Chdir(galeDir); err != nil {
		t.Fatal(err)
	}

	_ = gcCmd.RunE(gcCmd, nil)

	if _, err := os.Stat(
		filepath.Join(galeDir, ".gale"),
	); !os.IsNotExist(err) {
		t.Error("gc run from ~/.gale must not create a nested " +
			"~/.gale/.gale dir — the global config is not a " +
			"project config")
	}
}
