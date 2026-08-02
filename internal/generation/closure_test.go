package generation

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kelp/gale/internal/depsmeta"
)

// seedPkg creates a store dir for one package, optionally with
// dependency metadata.
func seedPkg(t *testing.T, storeRoot, name, version string, deps ...depsmeta.ResolvedDep) string {
	t.Helper()
	dir := filepath.Join(storeRoot, name, version)
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A binary, so a generation built over this package actually
	// creates the symlink the readers look for.
	if err := os.WriteFile(
		filepath.Join(dir, "bin", name), []byte("#!/bin/sh\n"), 0o755,
	); err != nil {
		t.Fatal(err)
	}
	if deps != nil {
		if err := depsmeta.Write(dir, depsmeta.Metadata{Deps: deps}); err != nil {
			t.Fatal(err)
		}
	}
	// The resolved spelling, because that is what every consumer of
	// these paths sees: the readers canonicalize so a caller comparing
	// against a directory it resolved itself cannot miss a match.
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// Design §13: a legacy lock records roots only, so a transitive
// dependency carries no hash — but the reference is still visible
// through the generation's links and each directory's dependency
// metadata. A destructive replacement has to see it.
func TestAuthoritativeClosureFollowsRecordedDeps(t *testing.T) {
	storeRoot := t.TempDir()
	dep := seedPkg(t, storeRoot, "oniguruma", "6.9-1")
	// The leaf records an explicitly empty closure, so the walk can
	// tell it apart from a directory that recorded nothing.
	if err := depsmeta.Write(dep, depsmeta.Metadata{}); err != nil {
		t.Fatal(err)
	}
	seedPkg(t, storeRoot, "jq", "1.7-1",
		depsmeta.ResolvedDep{Name: "oniguruma", Version: "6.9", Revision: 1})

	dirs, complete := AuthoritativeClosure(
		[]string{filepath.Join(storeRoot, "jq", "1.7-1")}, storeRoot,
	)
	if !complete {
		t.Error("every directory recorded its closure, so the walk is complete")
	}
	if !dirs[dep] {
		t.Errorf("transitive dep %s missing from the closure: %v", dep, dirs)
	}
}

// A directory that recorded nothing makes the closure unknown, not
// empty. depsmeta.Read collapses those two, which is why the scan
// cannot use it: a package with dependencies would read as a leaf and
// its deps would look unreferenced.
func TestAuthoritativeClosureReportsIncompleteOnMissingMetadata(t *testing.T) {
	storeRoot := t.TempDir()
	seedPkg(t, storeRoot, "jq", "1.7-1") // no metadata written

	dirs, complete := AuthoritativeClosure(
		[]string{filepath.Join(storeRoot, "jq", "1.7-1")}, storeRoot,
	)
	if complete {
		t.Error("a directory with no metadata leaves the closure unknown")
	}
	// The directory itself is still known to be referenced; only what
	// lies beyond it is unknown.
	if len(dirs) == 0 {
		t.Error("the linked directory is referenced regardless")
	}
}

// Malformed metadata is unknown too, and for the same reason.
func TestAuthoritativeClosureReportsIncompleteOnMalformedMetadata(t *testing.T) {
	storeRoot := t.TempDir()
	dir := seedPkg(t, storeRoot, "jq", "1.7-1")
	if err := os.WriteFile(
		filepath.Join(dir, depsmeta.File), []byte("{{{ not toml"), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	if _, complete := AuthoritativeClosure(
		[]string{filepath.Join(storeRoot, "jq", "1.7-1")}, storeRoot,
	); complete {
		t.Error("malformed metadata leaves the closure unknown")
	}
}

// A cycle terminates. Dependency metadata is written per directory and
// nothing forbids two packages recording each other.
func TestAuthoritativeClosureTerminatesOnACycle(t *testing.T) {
	storeRoot := t.TempDir()
	seedPkg(t, storeRoot, "a", "1-1",
		depsmeta.ResolvedDep{Name: "b", Version: "1", Revision: 1})
	seedPkg(t, storeRoot, "b", "1-1",
		depsmeta.ResolvedDep{Name: "a", Version: "1", Revision: 1})

	dirs, complete := AuthoritativeClosure(
		[]string{filepath.Join(storeRoot, "a", "1-1")}, storeRoot,
	)
	if !complete {
		t.Error("both directories recorded their closure")
	}
	if len(dirs) != 2 {
		t.Errorf("closure = %v, want both directories once", dirs)
	}
}

// The authoritative reader must return the directories the generation
// ACTUALLY links, not what re-resolving each name would pick. A
// generation linking a pre-revision bare dir while the canonical
// sibling also exists is exactly the upgrade-day shape design §13's
// veto is asked about, and re-resolution would silently protect the
// wrong directory.
func TestAuthoritativeGenerationDirsReturnsTheLinkedDir(t *testing.T) {
	storeRoot := t.TempDir()
	galeDir := filepath.Join(t.TempDir(), ".gale")
	bare := seedPkg(t, storeRoot, "jq", "1.7")

	// Built while only the bare directory existed, which is how a
	// pre-upgrade generation came to link one. Build resolves the
	// version itself, so the canonical sibling has to appear
	// afterwards for this to be the real shape rather than a
	// contrived one.
	if err := Build(map[string]string{"jq": "1.7"}, galeDir, storeRoot); err != nil {
		t.Fatal(err)
	}
	seedPkg(t, storeRoot, "jq", "1.7-1")

	dirs, err := AuthoritativeGenerationDirs(galeDir, storeRoot)
	if err != nil {
		t.Fatalf("AuthoritativeGenerationDirs: %v", err)
	}
	if len(dirs) != 1 {
		t.Fatalf("dirs = %v, want exactly the linked one", dirs)
	}
	if dirs[0] != bare {
		t.Errorf("dirs[0] = %q, want the linked %q (not the canonical "+
			"sibling re-resolution would pick)", dirs[0], bare)
	}
}

// A genuinely absent generation is an empty scope, not an error: a
// registered project that has never synced references nothing.
func TestAuthoritativeGenerationDirsAllowsAnAbsentGeneration(t *testing.T) {
	dirs, err := AuthoritativeGenerationDirs(
		filepath.Join(t.TempDir(), ".gale"), t.TempDir(),
	)
	if err != nil {
		t.Fatalf("absent generation must not error: %v", err)
	}
	if len(dirs) != 0 {
		t.Errorf("dirs = %v, want none", dirs)
	}
}

// A current symlink pointing at a generation that is not there is NOT
// an empty scope. The best-effort reader swallows the walk error and
// reports success with nothing found, which for a destructive
// decision reads as "this scope references nothing" — consent derived
// from a broken state.
func TestAuthoritativeGenerationDirsFailsOnADanglingCurrent(t *testing.T) {
	galeDir := filepath.Join(t.TempDir(), ".gale")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("gen/1", filepath.Join(galeDir, "current")); err != nil {
		t.Fatal(err)
	}

	if _, err := AuthoritativeGenerationDirs(galeDir, t.TempDir()); err == nil {
		t.Error("a current symlink with no generation behind it must " +
			"fail, not report an empty scope")
	}
}

// A stat that fails for a reason other than absence leaves the
// closure unknown. Absence is an answer — the directory is gone, so
// there is nothing to protect — but a permission error is not, and
// treating the two alike would report a scope as referencing nothing
// because its store was unreadable.
func TestAuthoritativeClosureReportsIncompleteOnAnUnstatableDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root stats regardless of mode")
	}
	storeRoot := t.TempDir()
	seedPkg(t, storeRoot, "jq", "1.7-1")

	// Deny traversal of the package directory, so stat of the version
	// directory fails with EACCES rather than ENOENT.
	pkgDir := filepath.Join(storeRoot, "jq")
	if err := os.Chmod(pkgDir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(pkgDir, 0o755) })

	if _, complete := AuthoritativeClosure(
		[]string{filepath.Join(storeRoot, "jq", "1.7-1")}, storeRoot,
	); complete {
		t.Error("an unstatable directory leaves the closure unknown")
	}
}

// The scan must follow the active `current` link, not rebuild a path
// from its number. Current parses the basename as an integer, so a
// current pointing at "alternate/1" yields 1, and reconstructing
// "gen/1" inspects a directory the scope is not using — approving
// replacement of bytes it loads through the real one.
func TestAuthoritativeGenerationDirsFollowsTheActualLink(t *testing.T) {
	storeRoot := t.TempDir()
	galeDir := filepath.Join(t.TempDir(), ".gale")
	live := seedPkg(t, storeRoot, "jq", "1.7-1")
	decoy := seedPkg(t, storeRoot, "ripgrep", "14-1")

	// A real generation at gen/1 linking the decoy...
	if err := Build(map[string]string{"ripgrep": "14-1"}, galeDir, storeRoot); err != nil {
		t.Fatal(err)
	}
	// ...and the active current pointing somewhere else entirely.
	alt := filepath.Join(galeDir, "alternate", "1", "bin")
	if err := os.MkdirAll(alt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Join(live, "bin", "jq"), filepath.Join(alt, "jq"),
	); err != nil {
		t.Fatal(err)
	}
	cur := filepath.Join(galeDir, "current")
	if err := os.Remove(cur); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("alternate/1", cur); err != nil {
		t.Fatal(err)
	}

	dirs, err := AuthoritativeGenerationDirs(galeDir, storeRoot)
	if err != nil {
		t.Fatalf("AuthoritativeGenerationDirs: %v", err)
	}
	found := map[string]bool{}
	for _, d := range dirs {
		found[d] = true
	}
	if !found[live] {
		t.Errorf("dirs = %v, want the directory the ACTIVE current "+
			"links (%s)", dirs, live)
	}
	if found[decoy] {
		t.Errorf("dirs = %v, must not include the inactive gen/1's %s",
			dirs, decoy)
	}
}

// Store directories come back in one spelling regardless of how the
// caller spelled the store root. On macOS /var is a symlink to
// /private/var, and a lexical comparison against the returned paths
// misses the directory it is protecting.
func TestAuthoritativeGenerationDirsCanonicalizesPaths(t *testing.T) {
	storeRoot := t.TempDir()
	resolved, err := filepath.EvalSymlinks(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if resolved == storeRoot {
		t.Skip("temp dir is not behind a symlink on this platform")
	}
	galeDir := filepath.Join(t.TempDir(), ".gale")
	seedPkg(t, storeRoot, "jq", "1.7-1")
	if err := Build(map[string]string{"jq": "1.7-1"}, galeDir, storeRoot); err != nil {
		t.Fatal(err)
	}

	raw, err := AuthoritativeGenerationDirs(galeDir, storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	viaResolved, err := AuthoritativeGenerationDirs(galeDir, resolved)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 1 || len(viaResolved) != 1 {
		t.Fatalf("raw=%v resolved=%v, want one each", raw, viaResolved)
	}
	if raw[0] != viaResolved[0] {
		t.Errorf("spelling of the store root changed the answer:\n"+
			"  %q\n  %q", raw[0], viaResolved[0])
	}
}

// ReferenceClosure stops AT the directory being replaced, and the
// stopping is what must be pinned rather than inferred.
//
// Excluding only that directory's own metadata would look identical
// in the command tests and be wrong: a candidate whose metadata is
// perfectly readable would still be descended through, its
// dependency judged, and a malformed one below it would refuse the
// replacement — which is the deadlock the exclusion exists to
// remove, arriving one level down.
//
// The second half is the other side of the same rule. Excluding the
// candidate is not permission to ignore that directory everywhere:
// reached from a root that is not being replaced, it is judged like
// any other.
func TestReferenceClosureStopsAtTheReplacedDir(t *testing.T) {
	storeRoot := t.TempDir()
	// A malformed child, reachable only through the candidate.
	child := seedPkg(t, storeRoot, "child", "1.0-1")
	if err := os.WriteFile(
		filepath.Join(child, depsmeta.File), []byte("not toml\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	// The candidate's own metadata is READABLE and names the child.
	candidate := seedPkg(t, storeRoot, "candidate", "1.0-1",
		depsmeta.ResolvedDep{Name: "child", Version: "1.0", Revision: 1})

	dirs, complete := ReferenceClosure(
		[]string{candidate}, storeRoot, candidate,
	)
	if !complete {
		t.Error("the walk descended through the directory being replaced")
	}
	if dirs[child] {
		t.Errorf("the child below the candidate entered the closure: %v", dirs)
	}
	if !dirs[candidate] {
		t.Errorf("the candidate itself is missing from the closure: %v", dirs)
	}

	// The same child, reached from a root that is not being replaced.
	other := seedPkg(t, storeRoot, "other", "1.0-1",
		depsmeta.ResolvedDep{Name: "child", Version: "1.0", Revision: 1})
	dirs, complete = ReferenceClosure(
		[]string{candidate, other}, storeRoot, candidate,
	)
	if complete {
		t.Error("a malformed directory reached from another root was " +
			"excused by the candidate's exemption")
	}
	if !dirs[child] {
		t.Errorf("the child is not reported as reachable: %v", dirs)
	}
}
