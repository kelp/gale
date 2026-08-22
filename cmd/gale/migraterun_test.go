package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
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
// seedRelocation builds a finished relocation: the pre-revision
// directory, the attested canonical one beside it, and optionally a
// scope that reaches the first or disagrees about the second.
//
// The global scope's gale dir IS galeHome, which in production is
// filepath.Dir(storeRoot). Nesting a ".gale" under it would build a
// generation projects.Scopes never looks at, and every veto would
// pass by seeing nothing.
func seedRelocation(
	t *testing.T, home, storeRoot string, referenced bool, lockSHA string,
) (string, migrateTarget) {
	t.Helper()
	bare := seedStore(t, storeRoot, "old", "1.0")
	writeDepsMeta(t, storeRoot, "old", "1.0")
	if referenced {
		if err := generation.Build(
			map[string]string{"old": "1.0"}, home, storeRoot,
		); err != nil {
			t.Fatal(err)
		}
	}
	if lockSHA != "" {
		proj := t.TempDir()
		if err := projects.Register(home, proj); err != nil {
			t.Fatal(err)
		}
		writeScopeLock(t, filepath.Join(proj, "gale.lock"),
			"old@1.0-1", lockSHA)
	}
	// The canonical artifact migrate would have installed, attested,
	// which the removal re-proves before it acts.
	seedProvenanced(t, storeRoot, "old", "1.0-1")
	r, err := migrateResolver("old")("old", "1.0-1")
	if err != nil {
		t.Fatal(err)
	}
	return bare, migrateTarget{
		name: "old", version: "1.0-1", dir: bare, bare: true, recipe: r,
	}
}

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
		bare:   true,
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

func hashOf(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}
