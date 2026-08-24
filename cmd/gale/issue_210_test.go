package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kelp/gale/internal/generation"
)

// gh#210: the remaining callers of the lenient generation reader.
//
// PR #239 added generation.CurrentVersionsStrict and adopted it in
// gc. These are the rest, and they do NOT all get the same posture.
// Three apply here:
//
//   - fail closed, for a decision that destroys bytes or grants
//     permission (the farm claims, the activation gate);
//   - fail toward work, for a decision about whether to redo work
//     (the recovery rebuild's skip, and live sync which rebuilds
//     from the v2 lock even when the active generation cannot
//     be read);
//   - fail loud but never abort, for a diagnostic (doctor).
//
// The tests below pin the second. The first lives beside the
// code it guards, in internal/generation and activation_test.go.

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

// End to end: sync must rebuild rather than abort.
//
// A broken gen/ that made `gale sync` refuse would leave the user
// with no way to fix it from inside gale. Live sync rebuilds from
// the v2 lock; an unreadable current is not a reason to skip.
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
	writeEmptyV2Lock(t, filepath.Join(proj, "gale.toml"))

	if err := runSync(syncRun{ProjectDir: proj}); err != nil {
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
