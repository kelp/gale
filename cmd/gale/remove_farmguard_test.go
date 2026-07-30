package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/kelp/gale/internal/depsmeta"
	"github.com/kelp/gale/internal/farm"
	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/projects"
)

// sonameFor returns the versioned dylib basename for the current
// OS, matching farm.IsVersionedDylib.
func sonameFor(stem string) string {
	if runtime.GOOS == "linux" {
		return stem + ".so.4"
	}
	return stem + ".4.dylib"
}

// removeGuardFixture is one machine with HOME isolated: project A
// (the cwd, about to run `gale remove`) has curl installed with a
// farmed dylib, and project B is registered as an external scope.
type removeGuardFixture struct {
	t        *testing.T
	home     string
	projA    string
	projB    string
	storeDir string // curl's store dir
	farmLink string // the shared farm entry for curl's soname
}

func newRemoveGuardFixture(t *testing.T) *removeGuardFixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	f := &removeGuardFixture{
		t:     t,
		home:  home,
		projA: filepath.Join(home, "proj-a"),
		projB: filepath.Join(home, "proj-b"),
	}

	// Project A: config pinning curl, minimal generation.
	if err := os.MkdirAll(f.projA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(f.projA, "gale.toml"),
		[]byte("[packages]\n  curl = \"8.19.0\"\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	galeDir := filepath.Join(f.projA, ".gale")
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

	// Installed curl with a farmed dylib in the shared farm.
	storeRoot := filepath.Join(home, ".gale", "pkg")
	f.storeDir = filepath.Join(storeRoot, "curl", "8.19.0-1")
	libDir := filepath.Join(f.storeDir, "lib")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	soname := sonameFor("libcurl")
	if err := os.WriteFile(
		filepath.Join(libDir, soname), []byte("x"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	farmDir := filepath.Join(home, ".gale", "lib")
	if err := farm.Populate(f.storeDir, farmDir); err != nil {
		t.Fatal(err)
	}
	f.farmLink = filepath.Join(farmDir, soname)

	// Project B: registered external scope.
	if err := os.MkdirAll(f.projB, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := projects.Register(
		filepath.Join(home, ".gale"), f.projB,
	); err != nil {
		t.Fatal(err)
	}

	chdirTo(t, f.projA)
	removeProject = true
	t.Cleanup(func() { removeProject = false })
	return f
}

// lockB gives project B a v1 lock rooted at the given identities.
func (f *removeGuardFixture) lockB(roots []string, pkgs map[string]lockfile.Package) {
	f.t.Helper()
	lf := &lockfile.V1{
		Version:  lockfile.SchemaVersion,
		Targets:  lockfile.Targets{Default: &lockfile.Target{Roots: roots}},
		Packages: pkgs,
	}
	if err := lockfile.WriteV1(
		filepath.Join(f.projB, "gale.lock"), lf,
	); err != nil {
		f.t.Fatal(err)
	}
}

// v1Node builds a minimal locked leaf node for the current
// platform.
func v1Node() lockfile.Package {
	return lockfile.Package{
		Artifacts: map[string]lockfile.Artifact{
			runtime.GOOS + "/" + runtime.GOARCH: {
				SHA256:      "deadbeef",
				Method:      "binary",
				GraphDigest: "digest",
			},
		},
	}
}

// TestRemoveRefusesClaimedFarmEntry (acceptance test 28, removal):
// `gale remove` depopulates the shared farm, so removing a package
// whose soname another scope's locked closure requires is refused
// — before the generation swap and before any farm or store
// mutation — naming the claimed version.
func TestRemoveRefusesClaimedFarmEntry(t *testing.T) {
	f := newRemoveGuardFixture(t)
	f.lockB([]string{"curl@8.19.0-1"}, map[string]lockfile.Package{
		"curl@8.19.0-1": v1Node(),
	})

	err := removeCmd.RunE(removeCmd, []string{"curl"})
	if err == nil {
		t.Fatal("remove of a claimed soname must be refused")
	}
	if !errors.Is(err, farm.ErrClaimConflict) {
		t.Fatalf("refusal must wrap ErrClaimConflict, got: %v", err)
	}
	if !strings.Contains(err.Error(), "curl@8.19.0-1") {
		t.Errorf("refusal %q must name the claimed version", err)
	}

	// Refused before any mutation of farm or store.
	if _, err := os.Readlink(f.farmLink); err != nil {
		t.Errorf("farm entry deleted despite refusal: %v", err)
	}
	if _, err := os.Stat(f.storeDir); err != nil {
		t.Errorf("store dir deleted despite refusal: %v", err)
	}
	// Refused before the generation swap: gen/1 is still current.
	target, err := os.Readlink(
		filepath.Join(f.projA, ".gale", "current"),
	)
	if err != nil || filepath.Base(target) != "1" {
		t.Errorf("current = %q (err %v), want gen/1 (no swap)",
			target, err)
	}
}

// TestRemoveRefusesWhenThisScopeStillNeedsIt: the initiating scope
// claims the closure the removal leaves behind, so removing a
// package another package in the SAME project links is refused.
//
// Nothing else catches this. The reference scan that decides
// whether to keep the store entry reads declared pins only
// (`addPackageRefs`, gc.go:639), so a package that is nobody's root
// and somebody's dependency reads as unreferenced; and the external
// claimant walk excludes the initiating scope by design. Both store
// dir and farm entry would go, leaving app resolving nothing.
func TestRemoveRefusesWhenThisScopeStillNeedsIt(t *testing.T) {
	f := newRemoveGuardFixture(t)
	storeRoot := filepath.Join(f.home, ".gale", "pkg")
	appDir := filepath.Join(storeRoot, "app", "1.0-1")
	if err := os.MkdirAll(filepath.Join(appDir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := depsmeta.Write(appDir, depsmeta.Metadata{
		Deps: []depsmeta.ResolvedDep{
			{Name: "curl", Version: "8.19.0", Revision: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(f.projA, "gale.toml"),
		[]byte("[packages]\n  app = \"1.0\"\n  curl = \"8.19.0\"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := generation.Build(
		map[string]string{"app": "1.0-1", "curl": "8.19.0-1"},
		filepath.Join(f.projA, ".gale"), storeRoot,
	); err != nil {
		t.Fatal(err)
	}

	err := removeCmd.RunE(removeCmd, []string{"curl"})
	if !errors.Is(err, farm.ErrClaimConflict) {
		t.Fatalf("removing a package this scope still links must be "+
			"refused, got: %v", err)
	}
	if _, lerr := os.Readlink(f.farmLink); lerr != nil {
		t.Errorf("farm entry deleted despite refusal: %v", lerr)
	}
	if _, serr := os.Stat(f.storeDir); serr != nil {
		t.Errorf("store dir deleted despite refusal: %v", serr)
	}
}

// TestRemoveRecheckssClaimsBesideTheDeletion: the early guard runs
// before the generation rebuild, which takes and releases the
// generation lock, so a claim established in that window would be
// deleted by a check that already passed. A check is worth only
// what it is atomic with, so the authoritative one runs inside the
// same lock hold as the deletion.
//
// beforeGuardedRemoval stands in for the other process: it fires
// inside that critical section, after the early check has already
// approved the removal.
func TestRemoveRechecksClaimsBesideTheDeletion(t *testing.T) {
	f := newRemoveGuardFixture(t)
	beforeGuardedRemoval = func() {
		f.lockB([]string{"curl@8.19.0-1"}, map[string]lockfile.Package{
			"curl@8.19.0-1": v1Node(),
		})
	}
	t.Cleanup(func() { beforeGuardedRemoval = nil })

	err := removeCmd.RunE(removeCmd, []string{"curl"})
	if !errors.Is(err, farm.ErrClaimConflict) {
		t.Fatalf("a claim established after the early check must "+
			"still refuse the deletion, got: %v", err)
	}
	if _, lerr := os.Readlink(f.farmLink); lerr != nil {
		t.Errorf("farm entry deleted despite the late claim: %v", lerr)
	}
	if _, serr := os.Stat(f.storeDir); serr != nil {
		t.Errorf("store dir deleted despite the late claim: %v", serr)
	}
}

// lockHeld reports whether some open file description holds an
// exclusive flock on path. Non-blocking, and flock is per open file
// description, so this answers correctly even for a lock this
// process holds elsewhere.
func lockHeld(t *testing.T, path string) bool {
	t.Helper()
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CREAT, 0o600)
	if err != nil {
		t.Fatalf("probe %s: %v", path, err)
	}
	defer syscall.Close(fd)
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return true
	}
	_ = syscall.Flock(fd, syscall.LOCK_UN)
	return false
}

// TestRemoveTakesTheVersionLockBeforeTheGenerationLock pins the lock
// ORDER, which neither function shows on its own.
//
// Store.Remove locks <root>/<name>/<version>.lock, the same file
// lockPackage holds for the whole of an install, and an install
// takes the generation lock underneath it at commitStaged. A
// removal that took them the other way round closes an AB-BA cycle
// on two blocking flock calls: `gale install foo` and `gale remove
// foo` wedge each other permanently.
//
// What this pins is COVERAGE, not order: the version lock is held
// for the whole guarded section, so the delete cannot land in a
// window where another process owns that version. Released before
// the delete, this interleaves — remove guards and depopulates, an
// installer commits a fresh copy of this exact version and
// repopulates the farm, and remove then deletes the directory the
// install just committed, leaving farm links pointing into nothing.
//
// The ORDER of the two locks is deliberately not tested here, and
// the distinction matters. Taking the generation lock first is a
// deadlock against the installer, not a data bug, and it only
// manifests under an interleaving no seam in this code can force:
// inside the section both locks are held, so a probe cannot tell
// the orders apart, and staging a stand-in installer still lets the
// two race for the generation lock. A test that stages it passes
// under the bug more often than not, which is worse than no test.
// The ordering rests on the argument in dropFromStore's comment and
// on store.RemoveWithin existing so a caller can put the version
// lock on the outside.
func TestRemoveHoldsTheVersionLockAcrossTheGuardedSection(t *testing.T) {
	f := newRemoveGuardFixture(t)
	versionLock := filepath.Join(
		f.home, ".gale", "pkg", "curl", "8.19.0-1.lock",
	)
	if lockHeld(t, versionLock) {
		t.Fatal("version lock held before the removal started")
	}
	var versionHeldInSection bool
	beforeGuardedRemoval = func() {
		versionHeldInSection = lockHeld(t, versionLock)
	}
	t.Cleanup(func() { beforeGuardedRemoval = nil })

	if err := removeCmd.RunE(removeCmd, []string{"curl"}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !versionHeldInSection {
		t.Error("the version lock was free inside the guarded section: " +
			"the delete is not covered by it, so an install committing " +
			"in that window is deleted afterwards")
	}
	if _, lerr := os.Lstat(f.farmLink); lerr == nil {
		t.Error("farm entry survived the removal")
	}
}

// TestRemoveRefusalLeavesConfigUnchanged: a guard refusal means the
// removal did not happen, so gale.toml must still declare the
// package. Otherwise the operation is neither applied nor reverted:
// the store, farm and generation still hold curl while the manifest
// that justifies them no longer names it, and the obvious retry
// reports the package as absent.
func TestRemoveRefusalLeavesConfigUnchanged(t *testing.T) {
	f := newRemoveGuardFixture(t)
	f.lockB([]string{"curl@8.19.0-1"}, map[string]lockfile.Package{
		"curl@8.19.0-1": v1Node(),
	})
	cfgPath := filepath.Join(f.projA, "gale.toml")
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := removeCmd.RunE(removeCmd, []string{"curl"}); !errors.Is(
		err, farm.ErrClaimConflict,
	) {
		t.Fatalf("remove must be refused, got: %v", err)
	}

	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf("gale.toml changed despite refusal:\nbefore:\n%s\nafter:\n%s",
			before, after)
	}
}

// TestRemoveRefusesGlobalLockedClaim (acceptance test 28): the
// global scope is a claimant too — registerProject skips it by
// design, so a registry-only claimant scan would miss it — and a
// project operation violating the global locked closure is refused
// like any other conflict.
func TestRemoveRefusesGlobalLockedClaim(t *testing.T) {
	f := newRemoveGuardFixture(t)
	lf := &lockfile.V1{
		Version:  lockfile.SchemaVersion,
		Targets:  lockfile.Targets{Default: &lockfile.Target{Roots: []string{"curl@8.19.0-1"}}},
		Packages: map[string]lockfile.Package{"curl@8.19.0-1": v1Node()},
	}
	if err := lockfile.WriteV1(
		filepath.Join(f.home, ".gale", "gale.lock"), lf,
	); err != nil {
		t.Fatal(err)
	}

	err := removeCmd.RunE(removeCmd, []string{"curl"})
	if !errors.Is(err, farm.ErrClaimConflict) {
		t.Fatalf("remove violating the global locked closure must "+
			"be refused, got: %v", err)
	}
	if _, lerr := os.Readlink(f.farmLink); lerr != nil {
		t.Errorf("farm entry deleted despite refusal: %v", lerr)
	}
}

// TestRemoveUnclaimedSucceedsWithClaimantPresent (acceptance test
// 37): an external claimant that does not claim the removed soname
// must not block the removal — presence is not conflict.
func TestRemoveUnclaimedSucceedsWithClaimantPresent(t *testing.T) {
	f := newRemoveGuardFixture(t)
	f.lockB([]string{"zstd@1.5.6-1"}, map[string]lockfile.Package{
		"zstd@1.5.6-1": v1Node(),
	})

	if err := removeCmd.RunE(removeCmd, []string{"curl"}); err != nil {
		t.Fatalf("remove of an unclaimed soname must succeed: %v", err)
	}
	if _, err := os.Lstat(f.farmLink); err == nil {
		t.Error("farm entry should be depopulated after remove")
	}
	if _, err := os.Stat(f.storeDir); err == nil {
		t.Error("store dir should be removed")
	}
}
