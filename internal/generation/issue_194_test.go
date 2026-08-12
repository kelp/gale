package generation

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/kelp/gale/internal/depsmeta"
	"github.com/kelp/gale/internal/farm"
)

// A legacy pre-revision install being replaced must not veto its own
// refresh (gh#194).
//
// proposedClaimant derives the walk's roots by round-tripping every
// placement through ChangedBy and resolveStoreDir. That round trip is
// not spelling-preserving: store resolution falls back from "1.0-1"
// to a bare "1.0" when the suffixed directory is absent, which is
// exactly the pre-commit state — the staged bytes have not been
// renamed in yet. The walk then queues the BARE directory while the
// staged map is keyed on the canonical one, the key never matches,
// and the superseded artifact enters the claim as though it were a
// committed peer of its own replacement.
//
// Three failures follow from the one mismatch, and this test pins all
// three: the superseded directory enters the claim, its dependencies
// decide the claim instead of the staged artifact's, and its sonames
// sit beside the placement's so the claim contradicts itself and the
// guard refuses the operation on the scope's own behalf — the verb
// veto design §4 forbids.
//
// No command reaches this today, and the reasons are worth writing
// down because they are what a future change would remove. Store.Create
// pre-creates the canonical directory before the guard runs, so the
// round trip finds an exact match on every ordinary install; `gale
// lock --refresh` refuses a pre-revision bare directory outright and
// names `gale migrate`; and migrate's relocating path defers the farm
// guard entirely, on the grounds that it must refuse. Un-defer any one
// of those and the deadlock is live, so the claim builder is fixed
// here rather than left resting on three unrelated accidents.
func TestProposedClaimantStaged_LegacyBareDirDoesNotVetoItsOwnRefresh(t *testing.T) {
	storeRoot := filepath.Join(t.TempDir(), "pkg")
	galeDir := filepath.Join(filepath.Dir(storeRoot), "gale")

	// The legacy install: a bare "1.0" directory, no revision suffix,
	// shipping the soname the refresh will carry forward.
	legacy := seedPkg(t, storeRoot, "root", "1.0")
	writeLib(t, legacy, soname("libroot"))

	// The replacement, staged, carrying the same soname and a
	// dependency the legacy artifact never recorded.
	dep := seedPkg(t, storeRoot, "newdep", "2.0-1")
	if err := depsmeta.Write(dep, depsmeta.Metadata{}); err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(t.TempDir(), ".build-194")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	writeLib(t, staging, soname("libroot"))
	if err := depsmeta.Write(staging, depsmeta.Metadata{
		Deps: []depsmeta.ResolvedDep{
			{Name: "newdep", Version: "2.0", Revision: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}

	placements := []farm.Placement{{
		ScanDir:  staging,
		FinalDir: filepath.Join(storeRoot, "root", "1.0-1"),
	}}
	c, err := ProposedClaimantStaged(placements, galeDir, storeRoot)
	if err != nil {
		t.Fatalf("ProposedClaimantStaged: %v", err)
	}
	if c.Err != nil {
		t.Fatalf("claim reported unreadable: %v", c.Err)
	}

	// Compared on CANONICALIZED spellings, because this is an
	// identity question. Raw string compares here pass on Linux and
	// go vacuous on macOS, where the same directory reached through
	// /var and /private/var is two strings.
	//
	// The superseded directory must not be claimed: the placement
	// carries that package, and claiming both spellings of it makes
	// the closure conflict with itself.
	if containsDir(canonicalClaimDirs(c), farm.CanonicalDir(legacy)) {
		t.Errorf("claim names the superseded %s beside its own "+
			"replacement; claims = %v", legacy, claimDirs(c))
	}
	// The staged artifact's own dependency must be walked. Reading the
	// legacy directory instead sees a package with no recorded deps.
	if !containsDir(canonicalClaimDirs(c), farm.CanonicalDir(dep)) {
		t.Errorf("claim omits the staged artifact's dependency %s; "+
			"claims = %v", dep, claimDirs(c))
	}

	// The whole point: the guard must let the scope refresh itself.
	if err := farm.GuardPopulate(
		placements, []farm.Claimant{c},
	); err != nil {
		t.Errorf("the scope vetoed its own refresh: %v", err)
		if errors.Is(err, farm.ErrClaimConflict) {
			t.Log("refusal is a claim conflict, i.e. a self-veto")
		}
	}
}

// writeLib drops one dylib into a directory's lib/, so the farm
// predicate enumerates a soname for it.
func writeLib(t *testing.T, dir, name string) {
	t.Helper()
	lib := filepath.Join(dir, "lib")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(lib, name), []byte("x"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
}

// canonicalClaimDirs is claimDirs under one spelling, for the
// identity comparisons above.
func canonicalClaimDirs(c farm.Claimant) []string {
	out := make([]string, 0, len(claimDirs(c)))
	for _, d := range claimDirs(c) {
		out = append(out, farm.CanonicalDir(d))
	}
	return out
}
