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

// TestRemoveLateRefusalRestoresTheLock closes the gap the pair
// review found in the compensation path.
//
// WriteLock commits the removal to gale.lock BEFORE the
// authoritative farm guard runs beside the deletion. So a late
// refusal that restored only gale.toml left the two disagreeing:
// the manifest names a package the lock no longer locks, which is
// precisely the stale-lock state, and the next sync fails on it.
// A refused operation must leave every file it touched as it found
// it, not merely the one that is easiest to put back.
func TestRemoveLateRefusalRestoresTheLock(t *testing.T) {
	f := newRemoveGuardFixture(t)
	lockPath := filepath.Join(f.projA, "gale.lock")

	// Project A starts with a lock naming curl, as a synced project
	// would.
	if err := lockfile.WriteV1(lockPath, &lockfile.V1{
		Version: lockfile.SchemaVersion,
		Targets: lockfile.Targets{
			Default: &lockfile.Target{Roots: []string{"curl@8.19.0-1"}},
		},
		Packages: map[string]lockfile.Package{"curl@8.19.0-1": v1Node()},
	}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}

	beforeGuardedRemoval = func() {
		f.lockB([]string{"curl@8.19.0-1"}, map[string]lockfile.Package{
			"curl@8.19.0-1": v1Node(),
		})
	}
	t.Cleanup(func() { beforeGuardedRemoval = nil })

	if err := removeCmd.RunE(removeCmd, []string{"curl"}); err == nil {
		t.Fatal("the late claim must refuse the removal")
	}

	after, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("lock missing after a refused removal: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("a refused removal rewrote gale.lock:\n"+
			"before:\n%s\nafter:\n%s", before, after)
	}
}

// TestRemoveLateRefusalRestoresAbsentLock is the other half of the
// snapshot: a project that was never locked must be left unlocked.
//
// Restoring an EMPTY lock instead of no lock would be worse than
// doing nothing. An empty document is a lock naming no roots, which
// is the stale-lock state against a gale.toml that still declares
// packages, so every later sync in that project would fail on a file
// a refused removal invented.
func TestRemoveLateRefusalRestoresAbsentLock(t *testing.T) {
	f := newRemoveGuardFixture(t)
	lockPath := filepath.Join(f.projA, "gale.lock")

	beforeGuardedRemoval = func() {
		f.lockB([]string{"curl@8.19.0-1"}, map[string]lockfile.Package{
			"curl@8.19.0-1": v1Node(),
		})
	}
	t.Cleanup(func() { beforeGuardedRemoval = nil })

	if err := removeCmd.RunE(removeCmd, []string{"curl"}); err == nil {
		t.Fatal("the late claim must refuse the removal")
	}

	if _, err := os.Lstat(lockPath); !errors.Is(err, os.ErrNotExist) {
		got, _ := os.ReadFile(lockPath)
		t.Errorf("a refused removal left a lock behind in a project "+
			"that had none (lstat %v):\n%s", err, got)
	}
}

// TestRemoveLateRefusalLeavesAConcurrentWriterAlone: the restore is
// a compare-and-swap, not a blind write.
//
// The snapshot is taken before this command's WriteLock and the
// refusal lands long after it, so another process can legitimately
// own gale.lock by then. Undoing our write by discarding theirs
// trades one inconsistency for a worse one: theirs is the state the
// machine is actually in. When the token no longer matches, the
// restore stands down and says so rather than overwriting.
func TestRemoveLateRefusalLeavesAConcurrentWriterAlone(t *testing.T) {
	f := newRemoveGuardFixture(t)
	lockPath := filepath.Join(f.projA, "gale.lock")
	concurrent := []byte("# written by another process\n")

	beforeGuardedRemoval = func() {
		f.lockB([]string{"curl@8.19.0-1"}, map[string]lockfile.Package{
			"curl@8.19.0-1": v1Node(),
		})
		// Stand in for the other process, after this command wrote
		// the lock and before the refusal restores it.
		if err := os.WriteFile(lockPath, concurrent, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { beforeGuardedRemoval = nil })

	err := removeCmd.RunE(removeCmd, []string{"curl"})
	if err == nil {
		t.Fatal("the late claim must refuse the removal")
	}
	if !strings.Contains(err.Error(), "changed since this command wrote it") {
		t.Errorf("refusal must report that the lock was left alone, "+
			"got: %v", err)
	}
	got, rerr := os.ReadFile(lockPath)
	if rerr != nil {
		t.Fatalf("read lock: %v", rerr)
	}
	if string(got) != string(concurrent) {
		t.Errorf("the concurrent writer's lock was overwritten:\n%s", got)
	}
}

// TestRemoveLateRefusalLeavesAConcurrentManifestAlone is the
// manifest twin of the lock case. A real concurrent command changes
// both files, so a compensation that restored gale.toml blindly
// while correctly leaving gale.lock alone would manufacture exactly
// the manifest/lock mismatch the lock restore exists to prevent,
// and would discard the other command's edit on the way.
func TestRemoveLateRefusalLeavesAConcurrentManifestAlone(t *testing.T) {
	f := newRemoveGuardFixture(t)
	cfgPath := filepath.Join(f.projA, "gale.toml")
	concurrent := []byte("[packages]\n  curl = \"8.19.0\"\n  jq = \"1.8.1\"\n")

	beforeGuardedRemoval = func() {
		f.lockB([]string{"curl@8.19.0-1"}, map[string]lockfile.Package{
			"curl@8.19.0-1": v1Node(),
		})
		if err := os.WriteFile(cfgPath, concurrent, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { beforeGuardedRemoval = nil })

	err := removeCmd.RunE(removeCmd, []string{"curl"})
	if err == nil {
		t.Fatal("the late claim must refuse the removal")
	}
	got, rerr := os.ReadFile(cfgPath)
	if rerr != nil {
		t.Fatalf("read config: %v", rerr)
	}
	if string(got) != string(concurrent) {
		t.Errorf("the concurrent command's gale.toml was overwritten "+
			"by the compensation:\n%s", got)
	}
	if !strings.Contains(err.Error(), "changed since this command wrote it") {
		t.Errorf("refusal must report the file it left alone, got: %v", err)
	}
}

// TestFileSnapshotDistinguishesAbsentFromEmpty pins the P2 finding
// directly, because the compare that depends on it is only reachable
// through a race the other tests stage.
//
// bytes.Equal(nil, []byte{}) is true, so a snapshot carrying only
// content cannot tell "no file" from "an empty file". A
// compare-and-swap built on one would treat a concurrent writer's
// empty file as still-ours and delete it.
func TestFileSnapshotDistinguishesAbsentFromEmpty(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "absent")
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	absentSnap, err := readFileSnapshot(missing)
	if err != nil {
		t.Fatal(err)
	}
	emptySnap, err := readFileSnapshot(empty)
	if err != nil {
		t.Fatal(err)
	}
	if absentSnap.Same(emptySnap) {
		t.Error("an absent file and an empty one compare equal: a " +
			"compare-and-swap on this would delete a concurrent " +
			"writer's empty file believing it was still ours")
	}
	if !absentSnap.Same(FileSnapshot{}) {
		t.Error("the zero snapshot must mean absent")
	}
}
