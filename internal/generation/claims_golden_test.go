package generation

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/kelp/gale/internal/depsmeta"
	"github.com/kelp/gale/internal/farm"
)

// Golden claim lists for the three ProposedClaimant entry points.
//
// A CHARACTERISATION net, not a repro: these record the exact
// placements each builder produces today, so a refactor that moves
// the claim onto a different representation has to keep producing
// them. They passed on main before any of this work.
//
// One fixture serves all three, because the point is the shape of
// the answer rather than a per-entry-point scenario: a scope that
// declares app, reaches zlib 1.3 through app's recorded deps, and is
// replacing app with a build that depends on zlib 2.0 instead.

// goldenScope is that fixture, plus the paths a golden refers to.
// SPELLING MATTERS HERE, and the golden pins which one each entry
// carries. A claim reports the path it was BUILT from, never a
// canonicalized rewrite of it: canonicalization decides identity and
// nothing else, so that a refusal message and a cross-claimant
// comparison do not change under a caller whose store root is
// reached through a symlink.
//
// So the builders legitimately differ, and on macOS the difference
// is visible because /var is a symlink to /private/var:
//
//   - ProposedClaimant claims through FarmStoreDirsStrict, which
//     joins the store root AS THE CALLER SPELLED IT. Its claim
//     carries the raw spelling.
//   - the staged builders claim the committed part of the closure
//     through the walk, which canonicalizes as it goes. That part
//     carries the resolved spelling, while the caller's own
//     placements carry whatever the caller supplied.
//
// EvalSymlinks on both sides of these comparisons would make them
// pass either way and so would stop pinning the property. The
// fixture therefore keeps both spellings and each golden names the
// one it expects.
type goldenScope struct {
	galeDir   string
	rawStore  string // the store root as the caller spells it
	storeRoot string // same directory, same spelling; what commands get
	app       string // app/1.0-1 resolved, the dir being replaced
	zlibOld   string // zlib/1.3-1 resolved, what the generation links
	zlibNew   string // zlib/2.0-1 resolved, what the staged app records
	staging   string // the replacement's bytes
}

// raw is a store dir spelled the way the caller spells the store
// root, which is what a claim built by joining it reports.
func (g goldenScope) raw(name, version string) string {
	return filepath.Join(g.rawStore, name, version)
}

func newGoldenScope(t *testing.T) goldenScope {
	t.Helper()
	root := t.TempDir()
	g := goldenScope{
		galeDir:   filepath.Join(root, ".gale"),
		rawStore:  filepath.Join(root, ".gale", "pkg"),
		storeRoot: filepath.Join(root, ".gale", "pkg"),
	}
	g.zlibOld = seedPkg(t, g.storeRoot, "zlib", "1.3-1")
	g.zlibNew = seedPkg(t, g.storeRoot, "zlib", "2.0-1")
	for _, d := range []string{g.zlibOld, g.zlibNew} {
		if err := depsmeta.Write(d, depsmeta.Metadata{}); err != nil {
			t.Fatal(err)
		}
	}
	g.app = seedPkg(t, g.storeRoot, "app", "1.0-1",
		depsmeta.ResolvedDep{Name: "zlib", Version: "1.3", Revision: 1})

	// app alone is declared. zlib is reached through app's recorded
	// deps, which is what lets a staged replacement change it: a
	// declared root survives every claim, a transitive dep survives
	// only while something still records it.
	if err := Build(map[string]string{
		"app": "1.0-1",
	}, g.galeDir, g.storeRoot); err != nil {
		t.Fatal(err)
	}

	// The replacement, staged: same identity, different dependency.
	g.staging = filepath.Join(t.TempDir(), ".build-golden")
	if err := os.MkdirAll(g.staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := depsmeta.Write(g.staging, depsmeta.Metadata{
		Deps: []depsmeta.ResolvedDep{
			{Name: "zlib", Version: "2.0", Revision: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}
	return g
}

// placements is the batch the staged builders are handed.
func (g goldenScope) placements() []farm.Placement {
	return []farm.Placement{{ScanDir: g.staging, FinalDir: g.app}}
}

// TestProposedClaimantGolden_Committed pins ProposedClaimant, whose
// caller has already committed its bytes (gale remove).
//
// Compared as a set: the walk seeds its queue from a map, so the
// order of this list is unspecified and the guard sorts by soname
// before it decides anything.
//
// The RAW spelling, deliberately: this claim is built by joining the
// store root the caller supplied, and nothing may canonicalize a
// reported path on the way out. Asserting the resolved spelling here
// passes on Linux and fails on macOS, where the two differ.
func TestProposedClaimantGolden_Committed(t *testing.T) {
	g := newGoldenScope(t)

	// Repointing zlib to 2.0 while app still records 1.3: the claim
	// names both, which is what makes the guard refuse the repoint.
	c, err := ProposedClaimant(
		map[string]string{"zlib": "2.0-1"}, g.galeDir, g.storeRoot,
	)
	if err != nil {
		t.Fatalf("ProposedClaimant: %v", err)
	}
	if c.Err != nil {
		t.Fatalf("claim reported unreadable: %v", c.Err)
	}
	if c.Label != "this scope" {
		t.Errorf("Label = %q, want %q", c.Label, "this scope")
	}
	want := []farm.Placement{
		{ScanDir: g.raw("app", "1.0-1"), FinalDir: g.raw("app", "1.0-1")},
		{ScanDir: g.raw("zlib", "1.3-1"), FinalDir: g.raw("zlib", "1.3-1")},
		{ScanDir: g.raw("zlib", "2.0-1"), FinalDir: g.raw("zlib", "2.0-1")},
	}
	assertPlacementSet(t, c.View.Placements(), want)
}

// TestProposedClaimantGolden_Staged pins both staged builders. They
// differ only in how they treat an unenumerable closure, and this
// fixture's closure enumerates, so the golden is one list.
//
// Order IS pinned here, and gh#194 changed it: the claim used to be
// the sorted committed dirs with the caller's placements appended,
// and is now the whole claim sorted by canonical directory. The SET
// is identical. The order is observable in exactly one place — which
// of two directories a self-conflict message names first — and it is
// deterministic both ways, so this is a presentation change and the
// only reason to record it is that a golden must not be vague.
func TestProposedClaimantGolden_Staged(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func([]farm.Placement, string, string) (farm.Claimant, error)
	}{
		{"ProposedClaimantStaged", ProposedClaimantStaged},
		{"ProposedClaimantRequired", ProposedClaimantRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGoldenScope(t)
			c, err := tc.build(g.placements(), g.galeDir, g.storeRoot)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if c.Err != nil {
				t.Fatalf("claim reported unreadable: %v", c.Err)
			}
			if c.Label != "this scope" {
				t.Errorf("Label = %q, want %q", c.Label, "this scope")
			}
			// zlib 2.0 because the STAGED metadata names it; zlib 1.3
			// is gone because the artifact that recorded it is the
			// artifact being replaced. The app placement reports the
			// destination the CALLER supplied; zlib comes from the
			// closure walk, which canonicalizes as it goes. Both are
			// as-supplied — neither is rewritten on the way out.
			want := []farm.Placement{
				{ScanDir: g.staging, FinalDir: g.app},
				{ScanDir: g.zlibNew, FinalDir: g.zlibNew},
			}
			if got := c.View.Placements(); !slices.Equal(got, want) {
				t.Errorf("claims = %+v, want %+v", got, want)
			}
		})
	}
}

// assertPlacementSet compares placements ignoring order.
func assertPlacementSet(t *testing.T, got, want []farm.Placement) {
	t.Helper()
	key := func(p farm.Placement) string {
		return p.ScanDir + "\x00" + p.FinalDir
	}
	gotKeys := make([]string, 0, len(got))
	for _, p := range got {
		gotKeys = append(gotKeys, key(p))
	}
	wantKeys := make([]string, 0, len(want))
	for _, p := range want {
		wantKeys = append(wantKeys, key(p))
	}
	slices.Sort(gotKeys)
	slices.Sort(wantKeys)
	if !slices.Equal(gotKeys, wantKeys) {
		t.Errorf("claims = %+v, want %+v (order ignored)", got, want)
	}
}
