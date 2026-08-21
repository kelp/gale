package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/lockplan"
	"github.com/kelp/gale/internal/projects"
)

// gh#210: the remaining callers of the lenient generation reader.
//
// PR #239 added generation.CurrentVersionsStrict and adopted it in
// gc. These are the rest, and they do NOT all get the same posture.
// Three apply here:
//
//   - fail closed, for a decision that destroys bytes or grants
//     permission (the farm claims, the activation gate, migrate's
//     relocation);
//   - fail toward work, for a decision about whether to redo work
//     (sync's drift, the recovery rebuild's skip);
//   - fail loud but never abort, for a diagnostic (doctor).
//
// The tests below pin the second and third. The first lives beside
// the code it guards, in internal/generation and activation_test.go.

// emptyGenerationTree leaves galeDir with a current pointer onto a
// generation directory that is not there, while gen/ itself stays a
// healthy directory.
//
// This is the shape breakGenerationWalk cannot express and sync
// needs: the walk fails, so the strict reader refuses, but a REBUILD
// still succeeds and can be asserted on. generation.Resolve names
// this exact state — an active generation "deleted out from under us
// by rm -rf, a partial gc, or a half-restored backup".
//
// Structural, not a chmod: CI and the agent container run tests as
// root and bypass permission bits.
func emptyGenerationTree(t *testing.T, galeDir string) {
	t.Helper()
	if err := os.RemoveAll(
		filepath.Join(galeDir, "gen", "1"),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Readlink(filepath.Join(galeDir, "current")); err != nil {
		t.Fatalf("the fixture needs a resolvable current pointer: %v", err)
	}
}

// An unreadable generation reads as DRIFT, so the rebuild runs and
// surfaces whatever is actually wrong.
//
// Today the lenient reader answers with an empty map, and against an
// emptied manifest an empty map compares equal: no drift, no
// rebuild, and the walk failure is never reported by anything. The
// emptied manifest is not a corner case — runSync calls it out by
// name, because "the generation still has to be rebuilt, or the last
// package's symlinks stay active in current/bin".
//
// The posture is deliberately not gc's. Aborting here would strand a
// user whose generation is broken in the one command that repairs
// it, so the read is strict and the answer on error stays true.
func TestGenerationDriftedReportsAnUnreadableGenerationAsDrift(t *testing.T) {
	tmp := t.TempDir()
	galeDir := filepath.Join(tmp, ".gale")
	storeRoot := filepath.Join(tmp, "pkg")

	seedStore(t, storeRoot, "jq", "1.7-1")
	if err := generation.Build(
		map[string]string{"jq": "1.7-1"}, galeDir, storeRoot,
	); err != nil {
		t.Fatal(err)
	}
	breakGenerationWalk(t, galeDir)

	if !generationDrifted(galeDir, storeRoot, map[string]string{}, nil) {
		t.Error("an unreadable generation was reported as matching " +
			"an emptied manifest: the rebuild is skipped and nothing " +
			"ever reports the walk failure")
	}
}

// The locked half of the same rule. A locked sync compares the
// active generation against the plan's roots rather than a recipe,
// and a plan rooting nothing is what a project has after its last
// package is removed and relocked.
func TestLockedGenerationDriftedReportsAnUnreadableGenerationAsDrift(t *testing.T) {
	tmp := t.TempDir()
	galeDir := filepath.Join(tmp, ".gale")
	storeRoot := filepath.Join(tmp, "pkg")

	seedStore(t, storeRoot, "jq", "1.7-1")
	if err := generation.Build(
		map[string]string{"jq": "1.7-1"}, galeDir, storeRoot,
	); err != nil {
		t.Fatal(err)
	}
	breakGenerationWalk(t, galeDir)

	if !lockedGenerationDrifted(galeDir, storeRoot, &lockplan.Plan{}) {
		t.Error("an unreadable generation was reported as matching " +
			"a plan rooting nothing; the sync leaves it in place")
	}
}

// End to end: sync must rebuild rather than abort.
//
// The whole reason this caller keeps its tolerant answer is that
// sync is the repair. A broken gen/ that made `gale sync` refuse
// would leave the user with no way to fix it from inside gale, which
// is worse than the fail-open being fixed.
func TestSyncRebuildsWhenTheActiveGenerationCannotBeRead(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GALE_OFFLINE", "1")
	storeRoot := filepath.Join(home, ".gale", "pkg")

	proj := t.TempDir()
	galeDir := filepath.Join(proj, ".gale")
	seedStore(t, storeRoot, "jq", "1.7-1")
	if err := generation.Build(
		map[string]string{"jq": "1.7-1"}, galeDir, storeRoot,
	); err != nil {
		t.Fatal(err)
	}
	// The manifest the user is left with after removing the last
	// package: sync must still rebuild, or jq stays on PATH.
	if err := os.WriteFile(
		filepath.Join(proj, "gale.toml"), []byte("[packages]\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	emptyGenerationTree(t, galeDir)

	if err := runSync("", false, false, false, proj); err != nil {
		t.Fatalf("sync must repair a broken generation, not refuse "+
			"to run in it: %v", err)
	}

	target, err := os.Readlink(filepath.Join(galeDir, "current"))
	if err != nil {
		t.Fatalf("reading current: %v", err)
	}
	if filepath.Base(target) == "1" {
		t.Errorf("current still points at the unreadable %s; sync "+
			"skipped the rebuild", target)
	}
	if _, err := os.Stat(filepath.Join(galeDir, target)); err != nil {
		t.Errorf("current points at %s, which does not exist: %v",
			target, err)
	}
}

// The recovery rebuild's skip takes the same tolerant posture.
//
// generationAlreadyLinks answers "is a rebuild unnecessary", and gc
// skips on true. An unreadable generation compared against a lock
// that roots nothing compares EQUAL today, so gc would skip the
// rebuild on exactly the machine that needs it. False is the safe
// direction and is what the caller was about to do anyway.
func TestGenerationAlreadyLinksIsFalseWhenTheGenerationIsUnreadable(t *testing.T) {
	tmp := t.TempDir()
	galeDir := filepath.Join(tmp, ".gale")
	storeRoot := filepath.Join(tmp, "pkg")

	seedStore(t, storeRoot, "jq", "1.7-1")
	if err := generation.Build(
		map[string]string{"jq": "1.7-1"}, galeDir, storeRoot,
	); err != nil {
		t.Fatal(err)
	}
	breakGenerationWalk(t, galeDir)

	if generationAlreadyLinks(galeDir, storeRoot, map[string]string{}) {
		t.Error("an unreadable generation was reported as already " +
			"linking the target set; gc would skip the " +
			"rebuild that would repair it")
	}
}

// Migrate's per-scope regeneration relocates bytes, so it fails
// closed.
//
// regenerateScope reads the scope's ACTIVE package set to follow its
// symlinks into the canonical directory. Read leniently, a scope
// whose generation cannot be walked comes back with nothing, hits
// the "never synced, nothing to move" branch, and is skipped in
// silence — after which the pass removes the pre-revision directory
// that scope's symlinks still name. It already errors when
// generation.Current fails; this is the same refusal one layer down.
//
// Both scopes, because migrate walks the global scope and every
// registered project through one code path and a cross-scope miss
// here destroys the bytes of whichever one was skipped.
func TestRegenerateScopeFailsClosedOnUnreadableGeneration(t *testing.T) {
	for _, scope := range []string{"global scope", "project scope"} {
		t.Run(scope, func(t *testing.T) {
			home := t.TempDir()
			storeRoot := filepath.Join(home, "pkg")

			galeDir := filepath.Join(home, ".gale")
			label := "the global scope"
			if scope == "project scope" {
				proj := filepath.Join(home, "proj")
				if err := os.MkdirAll(proj, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := projects.Register(galeDir, proj); err != nil {
					t.Fatal(err)
				}
				galeDir = filepath.Join(proj, ".gale")
				label = "project " + proj
			}

			seedStore(t, storeRoot, "old", "1.0")
			if err := generation.Build(
				map[string]string{"old": "1.0"}, galeDir, storeRoot,
			); err != nil {
				t.Fatal(err)
			}
			seedStore(t, storeRoot, "old", "1.0-1")
			breakGenerationWalk(t, galeDir)

			err := regenerateScope(projects.Scope{
				Label: label, GaleDir: galeDir,
			}, storeRoot, discardOutput())
			if err == nil {
				t.Fatal("a scope whose generation could not be read " +
					"was skipped as though it had never synced; the " +
					"pre-revision directory its symlinks name is then " +
					"removed")
			}
			for _, want := range []string{label, filepath.Join("gen", "1")} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error must name %s, got: %v", want, err)
				}
			}
		})
	}
}
