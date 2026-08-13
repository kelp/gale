package farm

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Property tests for ProposedStore. These are a SAFETY NET for the
// consolidation in gh#194, not a repro: they state the invariants
// the type exists to make unrepresentable, so a later reshape of it
// has to keep them.

// mkdirs creates each directory and returns them, so a fixture can
// stay one expression.
func mkdirs(t *testing.T, dirs ...string) []string {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dirs
}

// Building the same input twice produces the same view, and building
// a view from a view's own output changes nothing. Without this the
// type is a transformation rather than a representation, and a
// consumer could get a different answer for asking twice.
func TestProposedStoreConstructionIsIdempotent(t *testing.T) {
	root := t.TempDir()
	dirs := mkdirs(
		t,
		filepath.Join(root, "pkg", "a", "1.0-1"),
		filepath.Join(root, "pkg", "b", "2.0-1"),
	)
	staging := mkdirs(t, filepath.Join(root, ".build-a"))[0]
	placements := []Placement{{
		ScanDir: staging, FinalDir: filepath.Join(root, "pkg", "c", "3.0-1"),
	}}

	first, err := NewProposedStore(placements, dirs)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewProposedStore(placements, dirs)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(first.Dirs(), second.Dirs()) {
		t.Errorf("Dirs differ between builds: %v vs %v",
			first.Dirs(), second.Dirs())
	}
	if !slices.Equal(first.Placements(), second.Placements()) {
		t.Errorf("Placements differ between builds: %v vs %v",
			first.Placements(), second.Placements())
	}

	// Round trip: a view rebuilt from its own placements is itself.
	round, err := NewProposedStore(first.Placements(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(round.Placements(), first.Placements()) {
		t.Errorf("round trip changed the view: %v vs %v",
			round.Placements(), first.Placements())
	}
}

// A placement whose bytes are already at their canonical path is
// indistinguishable from a committed directory. This is what At
// builds, and reading one as staged would apply the staged-artifact
// tolerance to every package installed before dependency metadata
// existed.
func TestProposedStoreAtStylePlacementIsCommitted(t *testing.T) {
	root := t.TempDir()
	dir := mkdirs(t, filepath.Join(root, "pkg", "a", "1.0-1"))[0]

	viaPlacement, err := NewProposedStore(At(dir), nil)
	if err != nil {
		t.Fatal(err)
	}
	viaCommitted := Committed(dir)

	for _, tc := range []struct {
		name string
		view *ProposedStore
	}{
		{"placement", viaPlacement},
		{"committed", viaCommitted},
	} {
		path, staged, known := tc.view.ReadPath(dir)
		if !known {
			t.Fatalf("%s: view does not know %s", tc.name, dir)
		}
		if staged {
			t.Errorf("%s: a directory at its canonical path read as "+
				"staged", tc.name)
		}
		if path != dir {
			t.Errorf("%s: ReadPath = %s, want %s", tc.name, path, dir)
		}
	}
	if !slices.Equal(viaPlacement.Placements(), viaCommitted.Placements()) {
		t.Errorf("placement view %v differs from committed view %v",
			viaPlacement.Placements(), viaCommitted.Placements())
	}
}

// Two placements naming one canonical directory from different read
// paths is a construction error. No store state satisfies both, and
// last-wins would silently drop one scope's proposal.
func TestProposedStoreRefusesTwoReadPathsForOneDir(t *testing.T) {
	root := t.TempDir()
	final := filepath.Join(root, "pkg", "a", "1.0-1")
	one := mkdirs(t, filepath.Join(root, ".build-one"))[0]
	two := mkdirs(t, filepath.Join(root, ".build-two"))[0]

	_, err := NewProposedStore([]Placement{
		{ScanDir: one, FinalDir: final},
		{ScanDir: two, FinalDir: final},
	}, nil)
	if err == nil {
		t.Fatal("two read paths for one directory were merged " +
			"last-wins instead of refused")
	}
	for _, want := range []string{one, two} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}

	// The same placement twice is not a conflict: closures overlap.
	if _, err := NewProposedStore([]Placement{
		{ScanDir: one, FinalDir: final},
		{ScanDir: one, FinalDir: final},
	}, nil); err != nil {
		t.Errorf("a repeated placement read as a self-conflict: %v", err)
	}

	// Nor is a placement overriding a COMMITTED directory of the same
	// identity: that substitution is the whole point of the type.
	if _, err := NewProposedStore(
		[]Placement{{ScanDir: one, FinalDir: final}}, []string{final},
	); err != nil {
		t.Errorf("a placement superseding a committed dir was refused: %v",
			err)
	}
}

// Spelling permutations of one input produce equal views.
//
// Darwin-gated in practice: it needs a temp prefix that is itself a
// symlink, which is what macOS /var gives and Linux CI does not. The
// skip is the honest outcome there, not a silent pass — the property
// is real and only observable where two spellings exist.
func TestProposedStoreCanonicalizesSpelling(t *testing.T) {
	raw := t.TempDir()
	resolved, err := filepath.EvalSymlinks(raw)
	if err != nil {
		t.Fatal(err)
	}
	if resolved == raw {
		t.Skip("no symlinked temp prefix on this machine")
	}

	rawDir := filepath.Join(raw, "pkg", "a", "1.0-1")
	mkdirs(t, rawDir)
	resolvedDir := filepath.Join(resolved, "pkg", "a", "1.0-1")
	staging := mkdirs(t, filepath.Join(raw, ".build-a"))[0]

	// One directory, spelled one way as a placement and the other
	// as a committed dir.
	view, err := NewProposedStore(
		[]Placement{{ScanDir: staging, FinalDir: rawDir}},
		[]string{resolvedDir},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := view.Dirs(); len(got) != 1 {
		t.Fatalf("Dirs = %v, want one entry: two spellings of one "+
			"directory produced two entries", got)
	}
	if got := view.Dirs()[0]; got != resolvedDir {
		t.Errorf("Dirs[0] = %s, want the canonical %s", got, resolvedDir)
	}

	// Lookup works under either spelling, and both report staged.
	for _, spelling := range []string{rawDir, resolvedDir} {
		path, staged, known := view.ReadPath(spelling)
		if !known {
			t.Errorf("ReadPath(%s) unknown", spelling)
			continue
		}
		if !staged || path != staging {
			t.Errorf("ReadPath(%s) = (%s, staged=%v), want (%s, true)",
				spelling, path, staged, staging)
		}
	}

	// A placement whose two halves are spelled differently but name
	// one directory is still committed, not staged.
	same, err := NewProposedStore([]Placement{
		{ScanDir: rawDir, FinalDir: resolvedDir},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, staged, _ := same.ReadPath(rawDir); staged {
		t.Error("a placement reading its own directory through another " +
			"spelling read as staged")
	}
}

// The reported spellings are the caller's, never the canonical key.
// Targets and refusal messages are built from them, so canonicalizing
// identity must not rewrite an observation.
func TestProposedStoreReportsTheSuppliedSpelling(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "pkg", "a", "1.0-1")
	mkdirs(t, dir)
	staging := mkdirs(t, filepath.Join(root, ".build-a"))[0]

	view, err := NewProposedStore(
		[]Placement{{ScanDir: staging, FinalDir: dir}}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []Placement{{ScanDir: staging, FinalDir: dir}}
	if !slices.Equal(view.Placements(), want) {
		t.Errorf("Placements = %v, want %v", view.Placements(), want)
	}
}

// With layers onto a copy: the receiver is unchanged, so a view
// already handed to a guard cannot shift underneath it.
func TestProposedStoreWithDoesNotMutate(t *testing.T) {
	root := t.TempDir()
	base := Committed(mkdirs(t, filepath.Join(root, "pkg", "a", "1.0-1"))...)
	staging := mkdirs(t, filepath.Join(root, ".build-b"))[0]

	extended, err := base.With(Placement{
		ScanDir: staging, FinalDir: filepath.Join(root, "pkg", "b", "2.0-1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if base.Len() != 1 {
		t.Errorf("With mutated the receiver: Len = %d, want 1", base.Len())
	}
	if extended.Len() != 2 {
		t.Errorf("extended view Len = %d, want 2", extended.Len())
	}
	if base.Has(filepath.Join(root, "pkg", "b", "2.0-1")) {
		t.Error("With added an entry to the receiver")
	}
}
