package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/projects"
	"github.com/kelp/gale/internal/provenance"
)

// Regenerating a scope moves its symlinks from a pre-revision bare
// directory into the canonical one.
//
// This is the half of a relocation that no per-scope command may
// perform, and the reason design §13 hands pre-revision directories
// to machine-wide migrate: another scope's generation links the bare
// path, and moving the identity without repairing those symlinks
// would break a scope whose owner ran nothing.
//
// The package set comes from the ACTIVE generation, so it arrives as
// the bare "1.0" the link actually names, and store resolution
// carries it to the "1.0-1" that now exists beside it.
func TestRegenerateScopeFollowsIntoTheCanonicalDir(t *testing.T) {
	home := t.TempDir()
	storeRoot := filepath.Join(home, "pkg")
	galeDir := filepath.Join(home, ".gale")

	// The machine as it was before revisions existed.
	seedStore(t, storeRoot, "old", "1.0")
	if err := generation.Build(
		map[string]string{"old": "1.0"}, galeDir, storeRoot,
	); err != nil {
		t.Fatal(err)
	}
	if got := linkTarget(t, galeDir, "old"); !strings.Contains(
		got, filepath.Join("old", "1.0"),
	) {
		t.Fatalf("the fixture does not link the bare dir: %s", got)
	}

	// Migrate has installed the canonical artifact beside it.
	seedStore(t, storeRoot, "old", "1.0-1")

	if err := regenerateScope(projects.Scope{
		Label: "the global scope", GaleDir: galeDir,
	}, storeRoot, discardOutput()); err != nil {
		t.Fatal(err)
	}

	got := linkTarget(t, galeDir, "old")
	if !strings.Contains(got, filepath.Join("old", "1.0-1")) {
		t.Errorf("the generation still links %s, want the canonical dir", got)
	}
}

// linkTarget reads where a scope's active generation points one
// binary, without resolving it: the question is which store
// directory the link NAMES.
func linkTarget(t *testing.T, galeDir, name string) string {
	t.Helper()
	target, err := os.Readlink(filepath.Join(galeDir, "current", "bin", name))
	if err != nil {
		t.Fatal(err)
	}
	return target
}

// The pre-revision directory is removed only after the closure walk
// proves no scope reaches it.
//
// Rebuilding a generation moves the ROOTS that resolved to the bare
// directory. A transitive dependency is reached through a recorded
// closure rather than through any symlink, so a rebuild cannot move
// it, and deleting the directory anyway would break a dependent that
// resolves it at runtime. Leaving one directory behind is the honest
// outcome; destroying referenced bytes is not.
func TestRemoveRelocatedDirKeepsAReferencedDir(t *testing.T) {
	tests := []struct {
		name string
		// referenced puts the bare directory inside a scope's active
		// closure, which is the state that must stop the removal.
		referenced bool
		wantGone   bool
	}{
		{name: "a referenced dir survives", referenced: true, wantGone: false},
		{name: "an unreferenced dir is removed", referenced: false, wantGone: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			storeRoot := filepath.Join(home, "pkg")
			// The global scope's gale dir IS galeHome, which in
			// production is filepath.Dir(storeRoot). Nesting a ".gale"
			// under it would build a generation projects.Scopes never
			// looks at, and the veto would pass by seeing nothing.
			galeDir := home
			bare := seedStore(t, storeRoot, "old", "1.0")
			writeDepsMeta(t, storeRoot, "old", "1.0")
			if tt.referenced {
				if err := generation.Build(
					map[string]string{"old": "1.0"}, galeDir, storeRoot,
				); err != nil {
					t.Fatal(err)
				}
			}

			// The canonical artifact migrate would have installed,
			// attested, which the removal re-proves before it acts.
			seedProvenanced(t, storeRoot, "old", "1.0-1")
			r, rerr := migrateResolver("old")("old", "1.0-1")
			if rerr != nil {
				t.Fatal(rerr)
			}
			target := migrateTarget{
				name: "old", version: "1.0-1", dir: bare, recipe: r,
			}

			err := removeRelocatedDir(storeRoot, home, target, discardOutput())
			if tt.wantGone && err != nil {
				t.Fatalf("an unreferenced dir was not removed: %v", err)
			}
			if !tt.wantGone && !errors.Is(err, errBareDirStillReferenced) {
				t.Fatalf("err = %v, want errBareDirStillReferenced", err)
			}

			_, err = os.Lstat(bare)
			if tt.wantGone && err == nil {
				t.Error("an unreferenced pre-revision dir was left behind")
			}
			if !tt.wantGone && err != nil {
				t.Errorf("a referenced pre-revision dir was destroyed: %v", err)
			}
		})
	}
}

// A record that parses is not a record that attests the directory it
// sits in.
//
// This predicate authorizes deleting the pre-revision bytes, so it
// has to prove the canonical directory really holds the migrated
// artifact. Method and SHA alone do not: a record copied from
// another package carries the same declared hash whenever the
// fixture's recipes share one, and would authorize the deletion on
// the strength of a file somebody moved.
func TestCanonicalAttestsRejectsARecordForAnotherIdentity(t *testing.T) {
	home := t.TempDir()
	storeRoot := filepath.Join(home, "pkg")
	seedStore(t, storeRoot, "old", "1.0-1")
	// The record belongs to a different package and is written into
	// old's directory, which is what a copy or a partial restore
	// leaves behind.
	seedStore(t, storeRoot, "other", "1.0-1")
	writeProvenance(t, storeRoot, "other", "1.0-1")
	moveProvenance(t, storeRoot, "other", "old", "1.0-1")

	r, err := migrateResolver("old")("old", "1.0-1")
	if err != nil {
		t.Fatal(err)
	}
	target := migrateTarget{
		name: "old", version: "1.0-1",
		dir:    filepath.Join(storeRoot, "old", "1.0"),
		recipe: r,
	}

	if err := canonicalAttests(storeRoot, target); err == nil {
		t.Fatal("a record for another identity authorized the removal")
	}
}

// moveProvenance relocates a written record into another package's
// store directory, which no gale code path produces and a restore
// from a mixed-up backup does.
func moveProvenance(t *testing.T, storeRoot, from, to, version string) {
	t.Helper()
	src := filepath.Join(storeRoot, from, version, provenance.File)
	body, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(storeRoot, to, version, provenance.File)
	if err := os.WriteFile(dst, body, 0o644); err != nil {
		t.Fatal(err)
	}
}
