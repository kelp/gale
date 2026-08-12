package generation

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/depsmeta"
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

	if err := RebuildFarm(f.storeRoot, nil); err != nil {
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

	err := RebuildFarm(f.storeRoot, nil)
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

// Revalidation runs before the farm is touched, not after.
//
// The rebuild is itself a destructive commit under design §4: it
// wipes the farm and repopulates it, repointing sonames the whole
// machine resolves through. So §13's "revalidate before each
// destructive commit" applies to it, and the check has to happen
// inside the same lock acquisition — a caller that validated first
// and called afterwards would have released the lock in between,
// which is the window the rule exists to close.
//
// Without this, a lock that changed to another hash between the
// install and the rebuild is discovered only by the later removal
// check. That check preserves the pre-revision bytes, so the run
// looks safe, while the farm has already been repointed underneath
// the scope that disagreed.
func TestRebuildFarmValidatesBeforeItMutates(t *testing.T) {
	f := newClaimsFixture(t)
	bare := f.install("curl", "8.19.0", "libcurl")
	f.install("curl", "8.19.0-1", "libcurl")
	farmDir := farm.DirFromStoreRoot(f.storeRoot)
	if err := farm.Populate(bare, farmDir); err != nil {
		t.Fatal(err)
	}
	before, err := os.Readlink(filepath.Join(farmDir, soname("libcurl")))
	if err != nil {
		t.Fatal(err)
	}

	refused := errors.New("a scope changed its mind")
	err = RebuildFarm(f.storeRoot, func() error { return refused })
	if !errors.Is(err, refused) {
		t.Fatalf("err = %v, want the validation error", err)
	}

	after, rerr := os.Readlink(filepath.Join(farmDir, soname("libcurl")))
	if rerr != nil {
		t.Fatalf("a refused rebuild destroyed the farm entry: %v", rerr)
	}
	if after != before {
		t.Errorf("the farm moved to %s despite the refusal; was %s",
			after, before)
	}
}

// gh#184: the farm rebuild used to run entirely AFTER the
// current-symlink swap, so the fallible half of the operation sat
// past its own commit point. A generation could go live with a farm
// the rebuild then failed to build, and the farm was wiped before it
// was repopulated, so the failure left it destroyed rather than
// unchanged.
//
// The image is now staged before the swap and published after it,
// which splits the operation at the one place it can be split: every
// error a rebuild realistically hits (an unreadable store dir, a
// claim conflict, no space) happens while nothing has moved.

// A generation is not activated when its farm image cannot be built.
func TestBuildDoesNotActivateWhenTheFarmCannotBeBuilt(t *testing.T) {
	f := newClaimsFixture(t)
	curl := f.install("curl", "8.19.0-1", "libcurl")
	if err := Build(
		map[string]string{"curl": "8.19.0-1"}, f.galeHome, f.storeRoot,
	); err != nil {
		t.Fatal(err)
	}
	before, err := Current(f.galeHome)
	if err != nil {
		t.Fatal(err)
	}

	// The unreadable dylib provider is reached through recorded deps,
	// never as a generation entry: a broken lib dir on an entry is
	// caught by validateGenerationSymlinks, which would leave this
	// passing for a reason that has nothing to do with the farm.
	bad := filepath.Join(f.storeRoot, "zstd", "1.5.6-1")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(bad, "lib"), []byte("not a dir"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := depsmeta.Write(curl, depsmeta.Metadata{
		Deps: []depsmeta.ResolvedDep{
			{Name: "zstd", Version: "1.5.6", Revision: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}

	if err := Build(
		map[string]string{"curl": "8.19.0-1"}, f.galeHome, f.storeRoot,
	); err == nil {
		t.Fatal("Build activated a generation whose farm it could " +
			"not build")
	}
	after, err := Current(f.galeHome)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("current is generation %d, want the pre-build %d",
			after, before)
	}
}

// The farm resolves every entry the previous generation resolved for
// the whole interval the swap opens.
//
// A pin rather than a repro: it drives the seam the split
// establishes, so it cannot be run against the ordering it rules
// out. Its value is that a future rebuild which publishes before the
// swap, or which wipes the farm on its way through, is caught here.
func TestBuildKeepsTheFarmResolvableAcrossTheSwap(t *testing.T) {
	f := newClaimsFixture(t)
	curl := f.install("curl", "8.19.0-1", "libcurl")
	if err := Build(
		map[string]string{"curl": "8.19.0-1"}, f.galeHome, f.storeRoot,
	); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(
		farm.DirFromStoreRoot(f.storeRoot), soname("libcurl"),
	)

	var midSwap string
	var midErr error
	beforeFarmPublish = func() { midSwap, midErr = os.Readlink(entry) }
	t.Cleanup(func() { beforeFarmPublish = nil })

	f.install("zstd", "1.5.6-1", "libzstd")
	if err := Build(
		map[string]string{"curl": "8.19.0-1", "zstd": "1.5.6-1"},
		f.galeHome, f.storeRoot,
	); err != nil {
		t.Fatal(err)
	}

	if midErr != nil {
		t.Fatalf("the farm lost libcurl between the swap and the "+
			"publish: %v", midErr)
	}
	if want := filepath.Join(curl, "lib", soname("libcurl")); midSwap != want {
		t.Errorf("mid-swap farm entry = %q, want %q", midSwap, want)
	}
}

// A farm path that is not a directory refuses the build, before the
// swap, without destroying what sits there.
//
// The wipe used to hide this: RemoveAll deleted whatever occupied
// ~/.gale/lib — a file, a symlink pointing somewhere else — and
// MkdirAll then created the directory it wanted, so a broken layout
// was silently overwritten and the build reported success. Staging
// cannot overwrite it, so the farm directory is proved usable while
// the operation can still be refused for free.
func TestBuildRefusesAFarmPathThatIsNotADirectory(t *testing.T) {
	f := newClaimsFixture(t)
	f.install("curl", "8.19.0-1", "libcurl")
	if err := Build(
		map[string]string{"curl": "8.19.0-1"}, f.galeHome, f.storeRoot,
	); err != nil {
		t.Fatal(err)
	}
	before, err := Current(f.galeHome)
	if err != nil {
		t.Fatal(err)
	}

	farmDir := farm.DirFromStoreRoot(f.storeRoot)
	if err := os.RemoveAll(farmDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(farmDir, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}

	f.install("zstd", "1.5.6-1", "libzstd")
	if err := Build(
		map[string]string{"curl": "8.19.0-1", "zstd": "1.5.6-1"},
		f.galeHome, f.storeRoot,
	); err == nil {
		t.Fatal("Build activated a generation whose farm directory " +
			"could not be used")
	}
	after, err := Current(f.galeHome)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("current is generation %d, want the pre-build %d",
			after, before)
	}
	info, err := os.Lstat(farmDir)
	if err != nil {
		t.Fatalf("the refused build destroyed %s: %v", farmDir, err)
	}
	if info.IsDir() {
		t.Errorf("%s was replaced with a directory", farmDir)
	}
}
