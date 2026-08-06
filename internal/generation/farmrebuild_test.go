package generation

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/farm"
	"github.com/kelp/gale/internal/lockfile"
)

// The machine-wide rebuild establishes the farm every scope on the
// machine claims, with no scope exempt.
//
// `gale migrate` needs a rebuild no scope's own Build can perform. A
// relocation defers the farm past its commits, and a per-scope
// rebuild only reaches scopes with a generation to rebuild — a scope
// that requires the identity through its LOCK alone has none, so the
// farm would keep pointing into the pre-revision directory the pass
// then deletes.
func TestRebuildFarmFollowsALockOnlyClaim(t *testing.T) {
	f := newClaimsFixture(t)
	// The machine as migrate leaves it: the pre-revision directory,
	// the canonical one beside it, and a farm still describing the
	// first.
	bare := f.install("curl", "8.19.0", "libcurl")
	canonical := f.install("curl", "8.19.0-1", "libcurl")
	farmDir := farm.DirFromStoreRoot(f.storeRoot)
	if err := farm.Populate(bare, farmDir); err != nil {
		t.Fatal(err)
	}
	// The project requires the identity and has never synced, so it
	// has no generation for a per-scope rebuild to move.
	f.lockV1([]string{"curl@8.19.0-1"}, map[string]lockfile.Package{
		"curl@8.19.0-1": artifact("binary", nil, nil),
	})

	if err := RebuildFarm(f.storeRoot); err != nil {
		t.Fatalf("RebuildFarm: %v", err)
	}

	target, err := os.Readlink(filepath.Join(farmDir, soname("libcurl")))
	if err != nil {
		t.Fatalf("reading the farm entry: %v", err)
	}
	if !strings.HasPrefix(target, canonical+string(filepath.Separator)) {
		t.Errorf("the farm links %s, want the canonical %s", target, canonical)
	}
}

// A rebuild two scopes cannot agree on is refused, and refused with
// the farm untouched.
//
// The guard is the whole reason this runs under the generation lock
// rather than as a bare farm.Rebuild: the rebuild wipes the farm
// before it repopulates, so a set of dirs that cannot satisfy every
// claim would silently leave one scope's binaries resolving another
// scope's library. A refusal has to cost nothing, which is only true
// while nothing has moved.
func TestRebuildFarmRefusesADisagreement(t *testing.T) {
	f := newClaimsFixture(t)
	old := f.install("curl", "8.19.0-1", "libcurl")
	f.install("curl", "8.20.0-1", "libcurl")
	farmDir := farm.DirFromStoreRoot(f.storeRoot)
	if err := farm.Populate(old, farmDir); err != nil {
		t.Fatal(err)
	}
	// The global scope loads one version; the project requires the
	// other. No scope is exempt here, so the disagreement is visible
	// rather than decided by whichever scope happened to rebuild.
	if err := Build(
		map[string]string{"curl": "8.19.0-1"}, f.galeHome, f.storeRoot,
	); err != nil {
		t.Fatal(err)
	}
	f.lockV1([]string{"curl@8.20.0-1"}, map[string]lockfile.Package{
		"curl@8.20.0-1": artifact("binary", nil, nil),
	})

	err := RebuildFarm(f.storeRoot)
	if !errors.Is(err, farm.ErrClaimConflict) {
		t.Fatalf("err = %v, want farm.ErrClaimConflict", err)
	}
	target, rerr := os.Readlink(filepath.Join(farmDir, soname("libcurl")))
	if rerr != nil {
		t.Fatalf("the farm was mutated despite the refusal: %v", rerr)
	}
	if !strings.HasPrefix(target, old+string(filepath.Separator)) {
		t.Errorf("the farm links %s, want the pre-refusal %s", target, old)
	}
}

// soname is the versioned dylib basename claimsFixture.install
// writes for a stem, which is the farm entry it produces.
func soname(stem string) string {
	if runtime.GOOS == "linux" {
		return stem + ".so.4"
	}
	return stem + ".4.dylib"
}
