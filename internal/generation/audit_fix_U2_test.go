package generation

// Red-green tests for the U2 farm-reconcile audit unit:
//
//   gh#44 — Rollback never rebuilds the dylib farm, so after a
//   rollback the farm symlinks still point at the rolled-from
//   generation's revisions.
//
//   gh#43 — generation rebuild populates the farm only from the
//   config package set, wiping runtime-dep dylibs (recorded in
//   .gale-deps.toml) that dependents' rpaths resolve through.
//
//   gh#45 — Rollback validates the target gen's existence
//   outside the generation lock; a concurrent prune between
//   check and swap leaves current dangling.

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/kelp/gale/internal/depsmeta"
	"github.com/kelp/gale/internal/filelock"
)

// dylibNameU2 returns an OS-appropriate versioned dylib
// basename that farm.IsVersionedDylib accepts.
func dylibNameU2(stem, major string) string {
	if runtime.GOOS == "darwin" {
		return "lib" + stem + "." + major + ".dylib"
	}
	return "lib" + stem + ".so." + major
}

// createStoreEntryWithLibU2 creates a fake store entry with
// bin executables and lib dylibs. Returns the store dir.
func createStoreEntryWithLibU2(
	t *testing.T, storeRoot, name, version string,
	executables, dylibs []string,
) string {
	t.Helper()
	createStoreEntry(t, storeRoot, name, version, executables)
	dir := filepath.Join(storeRoot, name, version)
	libDir := filepath.Join(dir, "lib")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatalf("create lib dir: %v", err)
	}
	for _, d := range dylibs {
		if err := os.WriteFile(
			filepath.Join(libDir, d), []byte("fake dylib"), 0o644,
		); err != nil {
			t.Fatalf("create dylib %q: %v", d, err)
		}
	}
	return dir
}

// gh#44: every generation swap must leave the farm consistent
// with the rolled-to generation's package set. Rollback swaps
// current but historically never touched the farm, so binaries
// in the rolled-to gen resolved dylibs from the generation the
// user just rejected.
func TestRollbackRebuildsFarmFromTargetGeneration(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()
	lib := dylibNameU2("foo", "1")

	oldDir := createStoreEntryWithLibU2(
		t, storeRoot, "pkga", "1.0-1",
		[]string{"pkga"}, []string{lib},
	)
	if err := Build(
		map[string]string{"pkga": "1.0-1"}, galeDir, storeRoot,
	); err != nil {
		t.Fatalf("Build gen 1: %v", err)
	}

	newDir := createStoreEntryWithLibU2(
		t, storeRoot, "pkga", "1.0-2",
		[]string{"pkga"}, []string{lib},
	)
	if err := Build(
		map[string]string{"pkga": "1.0-2"}, galeDir, storeRoot,
	); err != nil {
		t.Fatalf("Build gen 2: %v", err)
	}

	// Precondition: after gen 2's build the farm points at
	// the new revision.
	farmLink := filepath.Join(galeDir, "lib", lib)
	target, err := os.Readlink(farmLink)
	if err != nil {
		t.Fatalf("read farm link after gen 2: %v", err)
	}
	if want := filepath.Join(newDir, "lib", lib); target != want {
		t.Fatalf("farm after gen 2 = %s, want %s", target, want)
	}

	if err := Rollback(galeDir, storeRoot, 1); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	target, err = os.Readlink(farmLink)
	if err != nil {
		t.Fatalf("read farm link after rollback: %v", err)
	}
	if want := filepath.Join(oldDir, "lib", lib); target != want {
		t.Errorf(
			"after rollback to gen 1 farm %s -> %s, want %s "+
				"(rollback did not rebuild the farm)",
			lib, target, want,
		)
	}
}

// gh#43: the farm must contain symlinks for every dylib any
// active binary may resolve at load time — including runtime
// deps recorded in .gale-deps.toml, which never appear in
// gale.toml. A rebuild from the config set alone wipes them.
func TestBuildFarmIncludesRecordedDeps(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()
	mainLib := dylibNameU2("main", "1")
	depLib := dylibNameU2("dep", "2")
	subLib := dylibNameU2("sub", "3")

	mainDir := createStoreEntryWithLibU2(
		t, storeRoot, "mainpkg", "1.0-1",
		[]string{"mainpkg"}, []string{mainLib},
	)
	depDir := createStoreEntryWithLibU2(
		t, storeRoot, "deppkg", "2.0-1",
		nil, []string{depLib},
	)
	subDir := createStoreEntryWithLibU2(
		t, storeRoot, "subpkg", "3.0-1",
		nil, []string{subLib},
	)

	// mainpkg was built against deppkg, which itself was
	// built against subpkg — recorded at install time.
	if err := depsmeta.Write(mainDir, depsmeta.Metadata{
		Deps: []depsmeta.ResolvedDep{
			{Name: "deppkg", Version: "2.0", Revision: 1},
		},
	}); err != nil {
		t.Fatalf("write mainpkg deps metadata: %v", err)
	}
	if err := depsmeta.Write(depDir, depsmeta.Metadata{
		Deps: []depsmeta.ResolvedDep{
			{Name: "subpkg", Version: "3.0", Revision: 1},
		},
	}); err != nil {
		t.Fatalf("write deppkg deps metadata: %v", err)
	}

	// Only mainpkg is in gale.toml; deps are runtime-only.
	if err := Build(
		map[string]string{"mainpkg": "1.0-1"}, galeDir, storeRoot,
	); err != nil {
		t.Fatalf("Build: %v", err)
	}

	for _, tc := range []struct {
		lib string
		dir string
	}{
		{mainLib, mainDir},
		{depLib, depDir},
		{subLib, subDir},
	} {
		target, err := os.Readlink(filepath.Join(galeDir, "lib", tc.lib))
		if err != nil {
			t.Errorf(
				"farm entry %s missing after gen rebuild: %v "+
					"(dep dylibs wiped by config-only farm rebuild)",
				tc.lib, err,
			)
			continue
		}
		if want := filepath.Join(tc.dir, "lib", tc.lib); target != want {
			t.Errorf("farm %s -> %s, want %s", tc.lib, target, want)
		}
	}
}

// gh#45: the target gen's existence check must happen under
// the generation lock. A concurrent prune (autoPruneGenerations
// after a Build) can delete the target while Rollback waits on
// the lock; checking outside lets the swap land a dangling
// current symlink while reporting success.
func TestRollbackChecksTargetGenUnderLock(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()
	createStoreEntry(t, storeRoot, "jq", "1.0", []string{"jq"})
	pkgs := map[string]string{"jq": "1.0"}
	if err := Build(pkgs, galeDir, storeRoot); err != nil {
		t.Fatalf("Build gen 1: %v", err)
	}
	if err := Build(pkgs, galeDir, storeRoot); err != nil {
		t.Fatalf("Build gen 2: %v", err)
	}

	// Hold the generation lock, exactly as a concurrent
	// Build + auto-prune would.
	lockPath := filepath.Join(filepath.Dir(storeRoot), "generation.lock")
	unlock, err := filelock.Acquire(lockPath)
	if err != nil {
		t.Fatalf("acquire generation lock: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- Rollback(galeDir, storeRoot, 1) }()

	// Let Rollback pass any pre-lock existence check and
	// block on the lock, then prune the target gen — the
	// interleaving auto-prune produces.
	time.Sleep(200 * time.Millisecond)
	if err := os.RemoveAll(
		filepath.Join(galeDir, "gen", "1"),
	); err != nil {
		t.Fatalf("prune target gen: %v", err)
	}
	unlock()

	if err := <-done; err == nil {
		target, _ := os.Readlink(filepath.Join(galeDir, "current"))
		t.Fatalf(
			"Rollback succeeded after the target gen was pruned; "+
				"current -> %s now dangles", target,
		)
	}

	// The failed rollback must leave current resolving.
	if _, err := os.Stat(filepath.Join(galeDir, "current")); err != nil {
		t.Errorf("current dangles after failed rollback: %v", err)
	}
}
