package generation

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/kelp/gale/internal/depsmeta"
	"github.com/kelp/gale/internal/farm"
)

// This file is a CHARACTERISATION net, not a repro. Every
// expectation here records what the closure walk does TODAY, so a
// refactor that reshapes the walk has to keep it doing exactly that.
// None of these ever failed on main; if one starts failing, the
// refactor changed behaviour and the refactor is wrong.
//
// The axes are the ones the walk actually branches on:
//
//   - how the root directory is presented: committed, staged
//     elsewhere, presented as a farm.At-style placement whose bytes
//     are already canonical, or absent entirely;
//   - what its dependency metadata says: recorded, absent, unusable;
//   - which interpretation is asking: the migration veto's
//     authoritative walk, its replacing-a-dir variant, the farm
//     populate claim, or the removal claim.
//
// The tolerance differences between the four are the whole subject.
// They are deliberate and each one is load-bearing, so they are
// pinned cell by cell rather than described in prose.

// dirKind is how the root directory is presented to the walk.
type dirKind int

const (
	// dirCommitted: bytes at the canonical path, nothing staged.
	dirCommitted dirKind = iota
	// dirStaged: canonical path empty, bytes in a staging directory
	// the caller substitutes.
	dirStaged
	// dirAtStyle: bytes at the canonical path AND named by a
	// placement whose ScanDir equals its FinalDir, which is what
	// farm.At builds. Must be indistinguishable from dirCommitted.
	dirAtStyle
	// dirAbsent: no bytes anywhere.
	dirAbsent
)

func (k dirKind) String() string {
	switch k {
	case dirCommitted:
		return "committed"
	case dirStaged:
		return "staged"
	case dirAtStyle:
		return "at-style"
	case dirAbsent:
		return "absent"
	default:
		return "dirKind(?)"
	}
}

// metaKind is what the root's dependency metadata says, written
// wherever the walk would read it.
type metaKind int

const (
	metaRecorded metaKind = iota // parses strictly, names the dep
	metaAbsent                   // no file at all
	metaUnusable                 // present and undecodable
)

func (k metaKind) String() string {
	switch k {
	case metaRecorded:
		return "recorded"
	case metaAbsent:
		return "absent"
	case metaUnusable:
		return "unusable"
	default:
		return "metaKind(?)"
	}
}

// result is one interpretation's answer, with dirs reduced to the
// fixture's symbolic names so the table stays readable.
type result struct {
	dirs     []string
	complete bool
}

// matrixFixture lays out one cell: a root "root@1.0-1" and a
// dependency "dep@2.0-1" that the root's metadata names when it is
// recorded. The dep always exists and always records an empty
// closure, so a "dep" in the answer means the walk descended.
type matrixFixture struct {
	storeRoot string
	root      string // canonical dir of the root, always
	dep       string
	staging   string // "" unless the kind stages bytes
}

func newMatrixFixture(t *testing.T, dk dirKind, mk metaKind) matrixFixture {
	t.Helper()
	storeRoot := filepath.Join(t.TempDir(), "pkg")
	dep := seedPkg(t, storeRoot, "dep", "2.0-1")
	if err := depsmeta.Write(dep, depsmeta.Metadata{}); err != nil {
		t.Fatal(err)
	}

	f := matrixFixture{storeRoot: storeRoot, dep: dep}
	rootPath := filepath.Join(storeRoot, "root", "1.0-1")

	switch dk {
	case dirCommitted, dirAtStyle:
		f.root = seedPkg(t, storeRoot, "root", "1.0-1")
		writeMeta(t, f.root, mk)
	case dirStaged:
		f.root = canonicalDir(rootPath) // absent: raw spelling
		f.staging = filepath.Join(t.TempDir(), ".build-matrix")
		if err := os.MkdirAll(f.staging, 0o755); err != nil {
			t.Fatal(err)
		}
		writeMeta(t, f.staging, mk)
	case dirAbsent:
		// Nothing on disk under either spelling; metadata is moot,
		// which every dirAbsent row records by answering alike.
		f.root = canonicalDir(rootPath)
	}
	return f
}

// writeMeta puts the requested metadata state into dir. metaRecorded
// names the fixture's dep; the walk descending to it is how a test
// tells "read the metadata" from "took it for a leaf".
func writeMeta(t *testing.T, dir string, mk metaKind) {
	t.Helper()
	switch mk {
	case metaRecorded:
		if err := depsmeta.Write(dir, depsmeta.Metadata{
			Deps: []depsmeta.ResolvedDep{
				{Name: "dep", Version: "2.0", Revision: 1},
			},
		}); err != nil {
			t.Fatal(err)
		}
	case metaAbsent:
		// Deliberately nothing.
	case metaUnusable:
		if err := os.WriteFile(
			filepath.Join(dir, depsmeta.File),
			[]byte("this is not toml\n"), 0o644,
		); err != nil {
			t.Fatal(err)
		}
	}
}

// placement is the fixture's root as the caller would present it.
func (f matrixFixture) placement() farm.Placement {
	scan := f.staging
	if scan == "" {
		scan = f.root
	}
	return farm.Placement{ScanDir: scan, FinalDir: f.root}
}

// stagedMap is the substitution map ProposedClosure takes today.
// dirCommitted and dirAbsent pass none; dirAtStyle passes an
// identity entry, which the walk must treat as no substitution.
func (f matrixFixture) stagedMap(dk dirKind) map[string]string {
	switch dk {
	case dirStaged, dirAtStyle:
		p := f.placement()
		return map[string]string{p.FinalDir: p.ScanDir}
	default:
		return nil
	}
}

// symbolic renames the walk's absolute dirs to the fixture's names,
// sorted, so a table can state expectations literally.
func (f matrixFixture) symbolic(dirs map[string]bool) []string {
	out := make([]string, 0, len(dirs))
	for _, d := range slices.Sorted(maps.Keys(dirs)) {
		switch d {
		case f.root:
			out = append(out, "root")
		case f.dep:
			out = append(out, "dep")
		default:
			out = append(out, d) // unexpected; fails loudly
		}
	}
	slices.Sort(out)
	return out
}

// matrixCase is one cell: an input shape and the answer each
// interpretation gives for it today.
type matrixCase struct {
	dk   dirKind
	mk   metaKind
	auth result // AuthoritativeClosure
	ref  result // ReferenceClosure, replacing the root itself
	prop result // ProposedClosure(require=false)
	req  result // ProposedClosure(require=true)
}

// closureMatrixCases is the whole cross product, one group per
// way of presenting the root directory.
func closureMatrixCases() []matrixCase {
	var out []matrixCase
	for _, group := range [][]matrixCase{
		committedCases(), stagedCases(), atStyleCases(), absentCases(),
	} {
		out = append(out, group...)
	}
	return out
}

// committedCases: bytes at the canonical path, nothing staged.
func committedCases() []matrixCase {
	return []matrixCase{
		// A committed root with a readable record is the ordinary
		// case: every interpretation descends, except the reference
		// walk, which stops at the directory it is replacing.
		{
			dk: dirCommitted, mk: metaRecorded,
			auth: result{[]string{"dep", "root"}, true},
			ref:  result{[]string{"root"}, true},
			prop: result{[]string{"dep", "root"}, true},
			req:  result{[]string{"dep", "root"}, true},
		},
		// No record on a COMMITTED directory splits the four. The
		// veto reads absence as an unknown closure and refuses; the
		// farm claim reads it as the leaf every pre-metadata package
		// is, because calling it unknown would refuse every
		// operation on a machine that has one.
		{
			dk: dirCommitted, mk: metaAbsent,
			auth: result{[]string{"root"}, false},
			ref:  result{[]string{"root"}, true},
			prop: result{[]string{"root"}, true},
			req:  result{[]string{"root"}, true},
		},
		// An undecodable record is unknown to everyone who reads it.
		// The reference walk still tolerates it, because it never
		// reads the directory it is replacing.
		{
			dk: dirCommitted, mk: metaUnusable,
			auth: result{[]string{"root"}, false},
			ref:  result{[]string{"root"}, true},
			prop: result{[]string{"root"}, false},
			req:  result{[]string{"root"}, false},
		},
	}
}

// stagedCases: canonical path empty, bytes elsewhere.
func stagedCases() []matrixCase {
	return []matrixCase{
		// Staged bytes stand in for a canonical path that does not
		// exist yet. The two walks that take no substitution map see
		// only the absence.
		{
			dk: dirStaged, mk: metaRecorded,
			auth: result{nil, true},
			ref:  result{nil, true},
			prop: result{[]string{"dep", "root"}, true},
			req:  result{[]string{"dep", "root"}, true},
		},
		// A staged artifact with no record is one the installer
		// commits anyway (design §7). The populate claim must not be
		// stricter than the commit it guards; the removal claim
		// must, because there bytes go away.
		{
			dk: dirStaged, mk: metaAbsent,
			auth: result{nil, true},
			ref:  result{nil, true},
			prop: result{[]string{"root"}, true},
			req:  result{[]string{"root"}, false},
		},
		{
			dk: dirStaged, mk: metaUnusable,
			auth: result{nil, true},
			ref:  result{nil, true},
			prop: result{[]string{"root"}, true},
			req:  result{[]string{"root"}, false},
		},
	}
}

// atStyleCases: a farm.At-style placement, whose bytes are already
// canonical. Every row must equal its committedCases twin.
func atStyleCases() []matrixCase {
	return []matrixCase{
		// A placement whose bytes are already canonical is an
		// ordinary committed directory. Every row below must match
		// its dirCommitted twin exactly — a substitution map entry
		// that made one look staged would apply the staged tolerance
		// to every package installed before metadata existed.
		{
			dk: dirAtStyle, mk: metaRecorded,
			auth: result{[]string{"dep", "root"}, true},
			ref:  result{[]string{"root"}, true},
			prop: result{[]string{"dep", "root"}, true},
			req:  result{[]string{"dep", "root"}, true},
		},
		{
			dk: dirAtStyle, mk: metaAbsent,
			auth: result{[]string{"root"}, false},
			ref:  result{[]string{"root"}, true},
			prop: result{[]string{"root"}, true},
			req:  result{[]string{"root"}, true},
		},
		{
			dk: dirAtStyle, mk: metaUnusable,
			auth: result{[]string{"root"}, false},
			ref:  result{[]string{"root"}, true},
			prop: result{[]string{"root"}, false},
			req:  result{[]string{"root"}, false},
		},
	}
}

// absentCases: no bytes anywhere.
func absentCases() []matrixCase {
	return []matrixCase{
		// An absent directory is nothing to protect for three of the
		// four, and an unsatisfiable claim for the removal walk.
		// Metadata cannot change that: all three rows agree.
		{
			dk: dirAbsent, mk: metaRecorded,
			auth: result{nil, true},
			ref:  result{nil, true},
			prop: result{nil, true},
			req:  result{nil, false},
		},
		{
			dk: dirAbsent, mk: metaAbsent,
			auth: result{nil, true},
			ref:  result{nil, true},
			prop: result{nil, true},
			req:  result{nil, false},
		},
		{
			dk: dirAbsent, mk: metaUnusable,
			auth: result{nil, true},
			ref:  result{nil, true},
			prop: result{nil, true},
			req:  result{nil, false},
		},
	}
}

// TestClosureInterpretationMatrix pins every cell of the walk's
// current behaviour. See the file comment: net, not repro.
func TestClosureInterpretationMatrix(t *testing.T) {
	for _, tc := range closureMatrixCases() {
		t.Run(tc.dk.String()+"/"+tc.mk.String(), func(t *testing.T) {
			f := newMatrixFixture(t, tc.dk, tc.mk)
			roots := []string{f.root}
			staged := f.stagedMap(tc.dk)

			for _, in := range []struct {
				name string
				want result
				run  func() (map[string]bool, bool)
			}{
				{"AuthoritativeClosure", tc.auth, func() (map[string]bool, bool) {
					return AuthoritativeClosure(roots, f.storeRoot)
				}},
				{"ReferenceClosure", tc.ref, func() (map[string]bool, bool) {
					return ReferenceClosure(roots, f.storeRoot, f.root)
				}},
				{"ProposedClosure(require=false)", tc.prop, func() (map[string]bool, bool) {
					return ProposedClosure(roots, f.storeRoot, staged, false)
				}},
				{"ProposedClosure(require=true)", tc.req, func() (map[string]bool, bool) {
					return ProposedClosure(roots, f.storeRoot, staged, true)
				}},
			} {
				dirs, complete := in.run()
				got := f.symbolic(dirs)
				if !slices.Equal(got, in.want.dirs) {
					t.Errorf("%s dirs = %v, want %v",
						in.name, got, in.want.dirs)
				}
				if complete != in.want.complete {
					t.Errorf("%s complete = %v, want %v",
						in.name, complete, in.want.complete)
				}
			}
		})
	}
}
