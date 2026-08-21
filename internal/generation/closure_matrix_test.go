package generation

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/kelp/gale/internal/depsmeta"
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
//     elsewhere, or absent entirely;
//   - what its dependency metadata says: recorded, absent, unusable;
//   - which interpretation is asking: the migration veto's
//     authoritative walk, or its replacing-a-dir variant.
//
// The tolerance differences between the two are the whole subject.
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
	// dirAbsent: no bytes anywhere.
	dirAbsent
)

func (k dirKind) String() string {
	switch k {
	case dirCommitted:
		return "committed"
	case dirStaged:
		return "staged"
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
	case dirCommitted:
		f.root = seedPkg(t, storeRoot, "root", "1.0-1")
		writeMeta(t, f.root, mk)
	case dirStaged:
		f.root = CanonicalDir(rootPath) // absent: raw spelling
		f.staging = filepath.Join(t.TempDir(), ".build-matrix")
		if err := os.MkdirAll(f.staging, 0o755); err != nil {
			t.Fatal(err)
		}
		writeMeta(t, f.staging, mk)
	case dirAbsent:
		// Nothing on disk under either spelling; metadata is moot,
		// which every dirAbsent row records by answering alike.
		f.root = CanonicalDir(rootPath)
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
}

// closureMatrixCases is the whole cross product, one group per
// way of presenting the root directory.
func closureMatrixCases() []matrixCase {
	var out []matrixCase
	for _, group := range [][]matrixCase{
		committedCases(), stagedCases(), absentCases(),
	} {
		out = append(out, group...)
	}
	return out
}

// committedCases: bytes at the canonical path, nothing staged.
func committedCases() []matrixCase {
	return []matrixCase{
		// A committed root with a readable record is the ordinary
		// case: the authoritative walk descends; the reference
		// walk stops at the directory it is replacing.
		{
			dk: dirCommitted, mk: metaRecorded,
			auth: result{[]string{"dep", "root"}, true},
			ref:  result{[]string{"root"}, true},
		},
		// No record on a COMMITTED directory: the veto reads
		// absence as an unknown closure and refuses.
		{
			dk: dirCommitted, mk: metaAbsent,
			auth: result{[]string{"root"}, false},
			ref:  result{[]string{"root"}, true},
		},
		// An undecodable record is unknown to everyone who reads it.
		// The reference walk still tolerates it, because it never
		// reads the directory it is replacing.
		{
			dk: dirCommitted, mk: metaUnusable,
			auth: result{[]string{"root"}, false},
			ref:  result{[]string{"root"}, true},
		},
	}
}

// stagedCases: canonical path empty, bytes elsewhere.
func stagedCases() []matrixCase {
	return []matrixCase{
		// Staged bytes stand in for a canonical path that does not
		// exist yet. The walks that take no substitution map see
		// only the absence.
		{
			dk: dirStaged, mk: metaRecorded,
			auth: result{nil, true},
			ref:  result{nil, true},
		},
		{
			dk: dirStaged, mk: metaAbsent,
			auth: result{nil, true},
			ref:  result{nil, true},
		},
		{
			dk: dirStaged, mk: metaUnusable,
			auth: result{nil, true},
			ref:  result{nil, true},
		},
	}
}

// absentCases: no bytes anywhere.
func absentCases() []matrixCase {
	return []matrixCase{
		{
			dk: dirAbsent, mk: metaRecorded,
			auth: result{nil, true},
			ref:  result{nil, true},
		},
		{
			dk: dirAbsent, mk: metaAbsent,
			auth: result{nil, true},
			ref:  result{nil, true},
		},
		{
			dk: dirAbsent, mk: metaUnusable,
			auth: result{nil, true},
			ref:  result{nil, true},
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
