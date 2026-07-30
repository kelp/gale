package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/farm"
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
