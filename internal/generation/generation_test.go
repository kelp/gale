package generation

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kelp/gale/internal/filelock"
)

// helper creates a fake store entry with executables.
// Layout: <storeRoot>/<name>/<version>/bin/<executables...>
func createStoreEntry(t *testing.T, storeRoot, name, version string, executables []string) {
	t.Helper()
	binDir := filepath.Join(storeRoot, name, version, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("failed to create store bin dir: %v", err)
	}
	for _, exe := range executables {
		path := filepath.Join(binDir, exe)
		if err := os.WriteFile(path, []byte("fake"), 0o755); err != nil {
			t.Fatalf("failed to create executable %q: %v", exe, err)
		}
	}
}

// --- Behavior 1: Build creates generation dir with bin/ symlinks ---

func TestBuildCreatesGenerationDirWithBinSymlinks(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	createStoreEntry(t, storeRoot, "jq", "1.8.1", []string{"jq"})
	createStoreEntry(t, storeRoot, "fd", "10.4.2", []string{"fd"})

	pkgs := map[string]string{
		"jq": "1.8.1",
		"fd": "10.4.2",
	}

	err := Build(pkgs, galeDir, storeRoot)
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}

	genBinDir := filepath.Join(galeDir, "gen", "1", "bin")
	info, err := os.Stat(genBinDir)
	if err != nil {
		t.Fatalf("generation bin dir does not exist: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("expected %q to be a directory", genBinDir)
	}

	// Verify symlinks exist for each executable.
	for _, exe := range []string{"jq", "fd"} {
		linkPath := filepath.Join(genBinDir, exe)
		linfo, err := os.Lstat(linkPath)
		if err != nil {
			t.Errorf("symlink %q does not exist: %v", exe, err)
			continue
		}
		if linfo.Mode()&os.ModeSymlink == 0 {
			t.Errorf("expected %q to be a symlink", linkPath)
		}
	}
}

func TestBuildSymlinksPointToStoreExecutables(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	createStoreEntry(t, storeRoot, "jq", "1.8.1", []string{"jq"})

	pkgs := map[string]string{"jq": "1.8.1"}

	err := Build(pkgs, galeDir, storeRoot)
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}

	linkPath := filepath.Join(galeDir, "gen", "1", "bin", "jq")
	target, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("failed to read symlink: %v", err)
	}

	// Resolve the symlink target relative to the link's directory
	// to get an absolute path for comparison.
	wantTarget := filepath.Join(storeRoot, "jq", "1.8.1", "bin", "jq")
	// Resolve both paths to handle macOS /var → /private/var.
	wantTarget, err = filepath.EvalSymlinks(wantTarget)
	if err != nil {
		t.Fatalf("failed to eval want target: %v", err)
	}
	resolved := target
	if !filepath.IsAbs(target) {
		resolved = filepath.Join(filepath.Dir(linkPath), target)
	}
	resolved, err = filepath.EvalSymlinks(resolved)
	if err != nil {
		t.Fatalf("failed to eval symlinks: %v", err)
	}
	if resolved != wantTarget {
		t.Errorf("symlink resolves to %q, want %q", resolved, wantTarget)
	}
}

func TestBuildLinksMultipleExecutablesFromOnePackage(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	createStoreEntry(t, storeRoot, "ripgrep", "14.1.0", []string{"rg", "rg-helper"})

	pkgs := map[string]string{"ripgrep": "14.1.0"}

	err := Build(pkgs, galeDir, storeRoot)
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}

	genBinDir := filepath.Join(galeDir, "gen", "1", "bin")
	for _, exe := range []string{"rg", "rg-helper"} {
		linkPath := filepath.Join(genBinDir, exe)
		linfo, err := os.Lstat(linkPath)
		if err != nil {
			t.Errorf("symlink %q does not exist: %v", exe, err)
			continue
		}
		if linfo.Mode()&os.ModeSymlink == 0 {
			t.Errorf("expected %q to be a symlink", linkPath)
		}
	}
}

// H5: Build skips (with a warning on stderr) packages whose
// store dir is missing — it does not error. The generation is
// committed without those packages so successfully installed
// packages still land on PATH (Issue #20).
func TestBuildSkipsMissingStorePackages(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	createStoreEntry(t, storeRoot, "jq", "1.8.1", []string{"jq"})

	pkgs := map[string]string{
		"jq":     "1.8.1",
		"awscli": "2.34.19",
	}

	// Build must succeed even though awscli is missing.
	if err := Build(pkgs, galeDir, storeRoot); err != nil {
		t.Fatalf("Build: %v", err)
	}

	// jq must be in the generation.
	if _, err := os.Lstat(filepath.Join(galeDir, "gen", "1", "bin", "jq")); err != nil {
		t.Fatalf("jq symlink missing: %v", err)
	}

	// awscli must not appear (it was skipped).
	if _, err := os.Lstat(filepath.Join(galeDir, "gen", "1", "bin", "aws")); !os.IsNotExist(err) {
		t.Fatalf("aws symlink should not exist, err=%v", err)
	}

	// current symlink must point at gen/1.
	if _, err := os.Lstat(filepath.Join(galeDir, "current")); err != nil {
		t.Fatalf("current symlink does not exist: %v", err)
	}
}

// H5: Build advances the generation even when some packages'
// store dirs are missing. The previous current symlink advances
// (not preserved) because a new generation was committed
// successfully with the packages that are on disk.
func TestBuildAdvancesCurrentWhenSomeStoreDirsMissing(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	createStoreEntry(t, storeRoot, "jq", "1.8.1", []string{"jq"})
	if err := Build(map[string]string{"jq": "1.8.1"}, galeDir, storeRoot); err != nil {
		t.Fatalf("initial Build: %v", err)
	}

	before, err := os.Readlink(filepath.Join(galeDir, "current"))
	if err != nil {
		t.Fatalf("read current before: %v", err)
	}

	// Build with fd missing — must succeed and advance current.
	pkgs := map[string]string{
		"jq": "1.8.1",
		"fd": "10.4.2",
	}
	if err := Build(pkgs, galeDir, storeRoot); err != nil {
		t.Fatalf("Build with missing fd: %v", err)
	}

	after, err := os.Readlink(filepath.Join(galeDir, "current"))
	if err != nil {
		t.Fatalf("read current after: %v", err)
	}
	if before == after {
		t.Errorf("current did not advance: before=%q after=%q (Build should have committed gen 2)",
			before, after)
	}
}

// Build carries a package forward from the previous
// generation when gale.toml pins a version that isn't in the
// store. Scenario: user edits gale.toml to a future version
// before the recipe lands in the registry, or the registry
// removes a version. Sync fails the install but the package
// is still in the store under its old version — without
// carry-forward, the old, working symlink is silently dropped
// from PATH (the original bug this guards against).
func TestBuildCarriesForwardMissingVersion(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	createStoreEntry(t, storeRoot, "starship", "1.24.2-2", []string{"starship"})
	createStoreEntry(t, storeRoot, "jq", "1.8.1", []string{"jq"})

	// Seed gen 1 with the working starship version so there is
	// a previous generation to carry forward from.
	prev := map[string]string{
		"starship": "1.24.2",
		"jq":       "1.8.1",
	}
	if err := Build(prev, galeDir, storeRoot); err != nil {
		t.Fatalf("seed Build error: %v", err)
	}

	// gale.toml now pins a version that doesn't exist in the
	// store. The previous gen had starship; carry it forward.
	next := map[string]string{
		"starship": "1.25.1",
		"jq":       "1.8.1",
	}
	if err := Build(next, galeDir, storeRoot); err != nil {
		t.Fatalf("Build error: %v", err)
	}

	link := filepath.Join(galeDir, "gen", "2", "bin", "starship")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("starship symlink missing in new gen: %v", err)
	}
	wantSuffix := filepath.Join("starship", "1.24.2-2", "bin", "starship")
	if !strings.HasSuffix(target, wantSuffix) {
		t.Errorf("starship target = %q, want suffix %q", target, wantSuffix)
	}

	// Sanity: current advanced to gen 2.
	cur, err := os.Readlink(filepath.Join(galeDir, "current"))
	if err != nil {
		t.Fatalf("read current: %v", err)
	}
	if filepath.Base(cur) != "2" {
		t.Errorf("current = %q, want gen/2", cur)
	}
}

// No previous generation means nothing to carry forward —
// Build must still silently skip the missing package,
// matching the Issue #20 contract.
func TestBuildSkipsWhenNoPreviousGeneration(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	createStoreEntry(t, storeRoot, "jq", "1.8.1", []string{"jq"})

	pkgs := map[string]string{
		"jq":       "1.8.1",
		"starship": "1.25.1",
	}
	if err := Build(pkgs, galeDir, storeRoot); err != nil {
		t.Fatalf("Build error: %v", err)
	}

	if _, err := os.Lstat(filepath.Join(galeDir, "gen", "1", "bin", "starship")); !os.IsNotExist(err) {
		t.Fatalf("starship should not exist (no prev gen), err=%v", err)
	}
}

// H5: defense in depth. Even when populateGeneration
// finishes without error, Build walks the new genDir's
// symlinks and fails the swap if any point at something
// that doesn't exist. Race safety for the case where the
// store dir went away between populate and rename.
func TestBuildFailsWhenGenerationHasDanglingSymlink(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	// Plant a package whose store layout embeds a symlink
	// pointing at a target that does not exist. The store
	// dir itself is present (so populateGeneration is happy),
	// but the link that gets copied through to the gen dir
	// points into the void.
	pkgDir := filepath.Join(storeRoot, "danglepkg", "1.0")
	binDir := filepath.Join(pkgDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The bin entry is a symlink into the store pointing at
	// a file that was never created. `symlinkDir` mirrors
	// this into the gen dir — our validate-before-swap must
	// catch it.
	if err := os.Symlink(
		filepath.Join(pkgDir, "libexec", "real"),
		filepath.Join(binDir, "danglepkg"),
	); err != nil {
		t.Fatal(err)
	}

	pkgs := map[string]string{"danglepkg": "1.0"}
	err := Build(pkgs, galeDir, storeRoot)
	if err == nil {
		t.Fatal("expected Build to reject a generation with dangling symlinks")
	}

	if _, err := os.Stat(filepath.Join(galeDir, "gen", "1")); !os.IsNotExist(err) {
		t.Errorf("gen/1 should be removed on validation failure, err=%v", err)
	}
	if _, err := os.Lstat(filepath.Join(galeDir, "current")); !os.IsNotExist(err) {
		t.Errorf("current symlink should not be created on validation failure, err=%v", err)
	}
}

// H5: symlinkDir used to return nil when its srcDir was
// missing. The caller (populateGeneration) already walks
// the parent with ReadDir, so a missing srcDir at this
// layer is a race (or a concurrent delete) and must fail
// rather than commit a generation with silently-dropped
// content.
func TestSymlinkDirErrorsWhenSrcDirMissing(t *testing.T) {
	dstDir := t.TempDir()
	srcDir := filepath.Join(t.TempDir(), "does-not-exist")

	err := symlinkDir(srcDir, dstDir, nil)
	if err == nil {
		t.Fatal("expected symlinkDir to error on missing srcDir")
	}
}

// --- Behavior 2: Build performs atomic swap of current symlink ---

func TestBuildCreatesCurrentSymlink(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	createStoreEntry(t, storeRoot, "jq", "1.8.1", []string{"jq"})

	pkgs := map[string]string{"jq": "1.8.1"}

	err := Build(pkgs, galeDir, storeRoot)
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}

	currentPath := filepath.Join(galeDir, "current")
	info, err := os.Lstat(currentPath)
	if err != nil {
		t.Fatalf("current symlink does not exist: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("expected %q to be a symlink", currentPath)
	}
}

func TestBuildCurrentSymlinkIsRelative(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	createStoreEntry(t, storeRoot, "jq", "1.8.1", []string{"jq"})

	pkgs := map[string]string{"jq": "1.8.1"}

	err := Build(pkgs, galeDir, storeRoot)
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}

	currentPath := filepath.Join(galeDir, "current")
	target, err := os.Readlink(currentPath)
	if err != nil {
		t.Fatalf("failed to read current symlink: %v", err)
	}
	if filepath.IsAbs(target) {
		t.Errorf("current symlink target %q should be relative", target)
	}
}

func TestBuildCurrentSymlinkPointsToNewGeneration(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	createStoreEntry(t, storeRoot, "jq", "1.8.1", []string{"jq"})

	pkgs := map[string]string{"jq": "1.8.1"}

	err := Build(pkgs, galeDir, storeRoot)
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}

	currentPath := filepath.Join(galeDir, "current")
	target, err := os.Readlink(currentPath)
	if err != nil {
		t.Fatalf("failed to read current symlink: %v", err)
	}
	if target != filepath.Join("gen", "1") {
		t.Errorf("current symlink = %q, want %q",
			target, filepath.Join("gen", "1"))
	}
}

func TestBuildUpdatesCurrentSymlinkOnSecondBuild(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	createStoreEntry(t, storeRoot, "jq", "1.8.1", []string{"jq"})
	createStoreEntry(t, storeRoot, "fd", "10.4.2", []string{"fd"})

	// First build.
	pkgs1 := map[string]string{"jq": "1.8.1"}
	if err := Build(pkgs1, galeDir, storeRoot); err != nil {
		t.Fatalf("first Build error: %v", err)
	}

	// Second build.
	pkgs2 := map[string]string{"jq": "1.8.1", "fd": "10.4.2"}
	if err := Build(pkgs2, galeDir, storeRoot); err != nil {
		t.Fatalf("second Build error: %v", err)
	}

	currentPath := filepath.Join(galeDir, "current")
	target, err := os.Readlink(currentPath)
	if err != nil {
		t.Fatalf("failed to read current symlink: %v", err)
	}
	if target != filepath.Join("gen", "2") {
		t.Errorf("current symlink = %q, want %q",
			target, filepath.Join("gen", "2"))
	}
}

// --- Behavior 3: Build retains previous generations ---

func TestBuildRetainsPreviousGenerationSinglePackage(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	createStoreEntry(t, storeRoot, "jq", "1.8.1", []string{"jq"})

	// First build creates generation 1.
	pkgs := map[string]string{"jq": "1.8.1"}
	if err := Build(pkgs, galeDir, storeRoot); err != nil {
		t.Fatalf("first Build error: %v", err)
	}

	gen1Dir := filepath.Join(galeDir, "gen", "1")
	if _, err := os.Stat(gen1Dir); err != nil {
		t.Fatalf("generation 1 should exist after first build: %v", err)
	}

	// Second build creates generation 2; generation 1 is retained.
	if err := Build(pkgs, galeDir, storeRoot); err != nil {
		t.Fatalf("second Build error: %v", err)
	}

	if _, err := os.Stat(gen1Dir); err != nil {
		t.Errorf("generation 1 should be retained: %v", err)
	}

	gen2Dir := filepath.Join(galeDir, "gen", "2")
	if _, err := os.Stat(gen2Dir); err != nil {
		t.Errorf("generation 2 should exist: %v", err)
	}
}

// --- Behavior 3b: Build retains previous generations ---

func TestBuildRetainsPreviousGeneration(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	createStoreEntry(t, storeRoot, "jq", "1.7.1", []string{"jq"})
	createStoreEntry(t, storeRoot, "fd", "9.0", []string{"fd"})

	// Build gen 1.
	if err := Build(map[string]string{"jq": "1.7.1"}, galeDir, storeRoot); err != nil {
		t.Fatalf("Build gen 1: %v", err)
	}

	// Build gen 2.
	if err := Build(map[string]string{"jq": "1.7.1", "fd": "9.0"}, galeDir, storeRoot); err != nil {
		t.Fatalf("Build gen 2: %v", err)
	}

	// Gen 1 should still exist.
	gen1Dir := filepath.Join(galeDir, "gen", "1")
	if _, err := os.Stat(gen1Dir); err != nil {
		t.Errorf("gen 1 was deleted but should be retained: %v", err)
	}

	// Current should be gen 2.
	cur, err := Current(galeDir)
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if cur != 2 {
		t.Errorf("expected current=2, got %d", cur)
	}
}

// --- Behavior 4: Current reads active generation number ---

func TestCurrentReturnsActiveGenerationNumber(t *testing.T) {
	galeDir := t.TempDir()

	// Manually set up a current symlink pointing to generation 3.
	gensDir := filepath.Join(galeDir, "gen", "3", "bin")
	if err := os.MkdirAll(gensDir, 0o755); err != nil {
		t.Fatalf("failed to create gen dir: %v", err)
	}
	if err := os.Symlink(
		filepath.Join("gen", "3"),
		filepath.Join(galeDir, "current"),
	); err != nil {
		t.Fatalf("failed to create current symlink: %v", err)
	}

	got, err := Current(galeDir)
	if err != nil {
		t.Fatalf("Current error: %v", err)
	}
	if got != 3 {
		t.Errorf("Current = %d, want 3", got)
	}
}

func TestCurrentReturnsZeroWhenNoCurrentExists(t *testing.T) {
	galeDir := t.TempDir()

	got, err := Current(galeDir)
	if err != nil {
		t.Fatalf("Current error: %v", err)
	}
	if got != 0 {
		t.Errorf("Current = %d, want 0", got)
	}
}

func TestCurrentReturnsErrorForNonNumericSymlinkTarget(t *testing.T) {
	galeDir := t.TempDir()

	// Create a current symlink pointing to a non-numeric generation.
	gensDir := filepath.Join(galeDir, "gen", "corrupt")
	if err := os.MkdirAll(gensDir, 0o755); err != nil {
		t.Fatalf("failed to create gen dir: %v", err)
	}
	if err := os.Symlink(
		filepath.Join("gen", "corrupt"),
		filepath.Join(galeDir, "current"),
	); err != nil {
		t.Fatalf("failed to create current symlink: %v", err)
	}

	_, err := Current(galeDir)
	if err == nil {
		t.Fatal("expected Current to return an error for non-numeric target")
	}
}

// --- Behavior 4b: Resolve verifies the generation directory exists ---

// TestResolveReturnsErrorForDanglingCurrentSymlink pins the
// invariant that Resolve treats a current symlink whose target
// gen directory is absent as an error — not as a healthy
// generation. `gale doctor` relies on this to catch the case
// where the active generation has been deleted out from under
// the current symlink (rm -rf, partial gc, restored backup).
func TestResolveReturnsErrorForDanglingCurrentSymlink(t *testing.T) {
	galeDir := t.TempDir()

	// Point current at gen/7 without ever creating gen/7.
	if err := os.Symlink(
		filepath.Join("gen", "7"),
		filepath.Join(galeDir, "current"),
	); err != nil {
		t.Fatalf("failed to create current symlink: %v", err)
	}

	gen, target, err := Resolve(galeDir)
	if err == nil {
		t.Fatalf("expected Resolve to error on dangling current, "+
			"got gen=%d target=%q", gen, target)
	}
	if !strings.Contains(err.Error(), "gen/7") &&
		!strings.Contains(err.Error(), "7") {
		t.Errorf("error should mention the missing target, got %v", err)
	}
}

// TestResolveSucceedsWhenTargetExists verifies the happy path:
// a current symlink pointing at an existing gen dir resolves to
// (n, target, nil).
func TestResolveSucceedsWhenTargetExists(t *testing.T) {
	galeDir := t.TempDir()

	if err := os.MkdirAll(
		filepath.Join(galeDir, "gen", "4", "bin"), 0o755,
	); err != nil {
		t.Fatalf("failed to create gen dir: %v", err)
	}
	if err := os.Symlink(
		filepath.Join("gen", "4"),
		filepath.Join(galeDir, "current"),
	); err != nil {
		t.Fatalf("failed to create current symlink: %v", err)
	}

	gen, target, err := Resolve(galeDir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if gen != 4 {
		t.Errorf("Resolve gen = %d, want 4", gen)
	}
	if target != filepath.Join("gen", "4") {
		t.Errorf("Resolve target = %q, want gen/4", target)
	}
}

// TestResolveReturnsZeroWhenNoCurrent matches Current's
// "no current symlink yet" contract — Resolve reports (0, "",
// nil) for fresh ~/.gale/ before the first install.
func TestResolveReturnsZeroWhenNoCurrent(t *testing.T) {
	galeDir := t.TempDir()

	gen, target, err := Resolve(galeDir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if gen != 0 {
		t.Errorf("Resolve gen = %d, want 0", gen)
	}
	if target != "" {
		t.Errorf("Resolve target = %q, want empty", target)
	}
}

// --- Behavior 6: Build creates generations/ dir if missing ---

func TestBuildCreatesGenerationsDirIfMissing(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	createStoreEntry(t, storeRoot, "jq", "1.8.1", []string{"jq"})

	// Verify generations/ does not exist yet.
	gensDir := filepath.Join(galeDir, "gen")
	if _, err := os.Stat(gensDir); !os.IsNotExist(err) {
		t.Fatalf("gen dir should not exist before Build")
	}

	pkgs := map[string]string{"jq": "1.8.1"}
	err := Build(pkgs, galeDir, storeRoot)
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}

	info, err := os.Stat(gensDir)
	if err != nil {
		t.Fatalf("gen dir should exist after Build: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("expected %q to be a directory", gensDir)
	}
}

// --- Behavior 7: Build works with empty package map ---

func TestBuildWithEmptyPackageMap(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	pkgs := map[string]string{}

	err := Build(pkgs, galeDir, storeRoot)
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}

	// Generation dir should exist with an empty bin/.
	genBinDir := filepath.Join(galeDir, "gen", "1", "bin")
	info, err := os.Stat(genBinDir)
	if err != nil {
		t.Fatalf("generation bin dir does not exist: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("expected %q to be a directory", genBinDir)
	}

	// bin/ should be empty.
	entries, err := os.ReadDir(genBinDir)
	if err != nil {
		t.Fatalf("failed to read bin dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("bin dir should be empty, got %d entries", len(entries))
	}
}

func TestBuildWithEmptyPackageMapCreatesCurrentSymlink(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	pkgs := map[string]string{}

	err := Build(pkgs, galeDir, storeRoot)
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}

	currentPath := filepath.Join(galeDir, "current")
	info, err := os.Lstat(currentPath)
	if err != nil {
		t.Fatalf("current symlink does not exist: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("expected %q to be a symlink", currentPath)
	}
}

// --- Behavior 8: Build symlinks root-level files ---

func TestBuildSymlinksRootLevelFiles(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	// Create a package with root-level files (like Go's
	// go.env and VERSION).
	pkgDir := filepath.Join(storeRoot, "go", "1.26.1")
	if err := os.MkdirAll(
		filepath.Join(pkgDir, "bin"), 0o755,
	); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(pkgDir, "bin", "go"),
		[]byte("fake"), 0o755)
	os.WriteFile(filepath.Join(pkgDir, "go.env"),
		[]byte("GOPROXY=https://proxy.golang.org,direct\n"),
		0o644)
	os.WriteFile(filepath.Join(pkgDir, "VERSION"),
		[]byte("go1.26.1"), 0o644)

	pkgs := map[string]string{"go": "1.26.1"}
	if err := Build(pkgs, galeDir, storeRoot); err != nil {
		t.Fatalf("Build error: %v", err)
	}

	// Root-level files should be symlinked into the
	// generation directory.
	for _, name := range []string{"go.env", "VERSION"} {
		path := filepath.Join(galeDir, "current", name)
		info, err := os.Lstat(path)
		if err != nil {
			t.Errorf("root file %q not symlinked: %v",
				name, err)
			continue
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("expected %q to be a symlink", path)
		}
	}
}

// --- Behavior 9: Build symlinks lib, man, include ---

func TestBuildSymlinksLibDir(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	// Create a package with lib/ contents.
	pkgDir := filepath.Join(storeRoot, "pkgconf", "2.5.1")
	for _, sub := range []string{"bin", "lib"} {
		if err := os.MkdirAll(
			filepath.Join(pkgDir, sub), 0o755,
		); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(pkgDir, "bin", "pkgconf"),
		[]byte("fake"), 0o755)
	os.WriteFile(filepath.Join(pkgDir, "lib", "libpkgconf.7.dylib"),
		[]byte("fake"), 0o755)

	pkgs := map[string]string{"pkgconf": "2.5.1"}
	if err := Build(pkgs, galeDir, storeRoot); err != nil {
		t.Fatalf("Build error: %v", err)
	}

	genLib := filepath.Join(galeDir, "current", "lib",
		"libpkgconf.7.dylib")
	info, err := os.Lstat(genLib)
	if err != nil {
		t.Fatalf("lib symlink not found: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("expected symlink at %q", genLib)
	}
}

func TestBuildSymlinksManSubdirs(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	pkgDir := filepath.Join(storeRoot, "jq", "1.8.1")
	for _, sub := range []string{"bin", "man/man1"} {
		if err := os.MkdirAll(
			filepath.Join(pkgDir, sub), 0o755,
		); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(pkgDir, "bin", "jq"),
		[]byte("fake"), 0o755)
	os.WriteFile(filepath.Join(pkgDir, "man", "man1", "jq.1"),
		[]byte("fake"), 0o644)

	pkgs := map[string]string{"jq": "1.8.1"}
	if err := Build(pkgs, galeDir, storeRoot); err != nil {
		t.Fatalf("Build error: %v", err)
	}

	genMan := filepath.Join(galeDir, "current", "man",
		"man1", "jq.1")
	info, err := os.Lstat(genMan)
	if err != nil {
		t.Fatalf("man symlink not found: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("expected symlink at %q", genMan)
	}
}

// --- Behavior 10: Deterministic bin-collision refusal ---

// TestBuildDeterministicSymlinkOrder keeps its original intent —
// two packages shipping one basename must produce the same outcome
// on every run — and re-anchors it to the outcome gh#190 fixed.
// Sort order used to hand the name to whichever package sorted
// first, silently. The build is now refused, and the refusal must
// be identical every time: same error type, same two providers,
// same rendered message.
func TestBuildDeterministicSymlinkOrder(t *testing.T) {
	var first string
	for i := range 20 {
		galeDir := t.TempDir()
		storeRoot := t.TempDir()

		createStoreEntry(t, storeRoot, "alpha", "1.0", []string{"tool"})
		createStoreEntry(t, storeRoot, "beta", "1.0", []string{"tool"})

		pkgs := map[string]string{
			"alpha": "1.0",
			"beta":  "1.0",
		}

		err := Build(pkgs, galeDir, storeRoot)
		var collErr *BinCollisionError
		if !errors.As(err, &collErr) {
			t.Fatalf("iteration %d: error = %v (%T), want "+
				"*BinCollisionError", i, err, err)
		}
		want := []BinCollision{
			{Bin: "tool", Existing: "alpha", Incoming: "beta"},
		}
		if !slices.Equal(collErr.Collisions, want) {
			t.Fatalf("iteration %d: collisions = %+v, want %+v",
				i, collErr.Collisions, want)
		}
		if _, statErr := os.Lstat(
			filepath.Join(galeDir, "current"),
		); !os.IsNotExist(statErr) {
			t.Fatalf("iteration %d: current symlink exists (err=%v); "+
				"a refused build must not activate anything",
				i, statErr)
		}

		if i == 0 {
			first = err.Error()
			continue
		}
		if err.Error() != first {
			t.Fatalf("iteration %d: message = %q, want %q — the "+
				"refusal must not vary with map order", i, err.Error(), first)
		}
	}
}

// --- Behavior 11: Unique temp-link path for concurrent builds ---

// deadlockBackstop bounds a positive assertion that waits on
// genuinely asynchronous work — a Build running in another
// goroutine, a contender parked in flock. It exists so a real
// deadlock fails naming what hung, instead of running the package
// out to the go test timeout.
//
// It is not a performance budget, and must not be tuned to
// expected timing. Goroutine handoff plus filelock.Acquire on a
// free lock peaked at 3.1 ms over 2000 iterations at load 12
// (measured for gh#246); what made these deadlines flake was the
// rare multi-second stall — race-detector stop-the-world,
// container CPU throttling, an I/O stall inside Acquire's
// MkdirAll/OpenFile. A margin sized against the steady-state cost
// converts that slowness into a failure, so this is sized to be
// unreachable by anything short of a hang (gh#251). It matches
// the backstops internal/installer already uses.
//
// A wait that has a real synchronisation point wants no clock at
// all: when the operation under test has already returned, probe
// the lock with a non-blocking flock instead (gh#246, gh#250).
// Neither use below has that option — one waits for a Build to
// finish, the other for a blocked contender to wake, and a
// non-blocking probe cannot tell a lock a live contender just
// took from one that was never released.
const deadlockBackstop = 30 * time.Second

func TestBuildWaitsForGenerationLock(t *testing.T) {
	galeDir := t.TempDir()
	// storeRoot must be a child of galeDir so that
	// filepath.Dir(storeRoot) == galeDir — Build acquires
	// the lock at filepath.Dir(storeRoot)/generation.lock,
	// which equals galeDir/generation.lock here.
	storeRoot := filepath.Join(galeDir, "pkg")
	if err := os.MkdirAll(storeRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	createStoreEntry(t, storeRoot, "jq", "1.8.1", []string{"jq"})

	unlock, err := filelock.Acquire(filepath.Join(galeDir, "generation.lock"))
	if err != nil {
		t.Fatalf("Acquire lock: %v", err)
	}
	defer unlock()

	done := make(chan error, 1)
	go func() {
		done <- Build(map[string]string{"jq": "1.8.1"}, galeDir, storeRoot)
	}()

	select {
	case err := <-done:
		t.Fatalf("Build completed while generation lock was held: %v", err)
	case <-time.After(50 * time.Millisecond):
		// Good: Build is blocked on the generation lock.
	}

	unlock()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Build error after lock release: %v", err)
		}
	case <-time.After(deadlockBackstop):
		t.Fatal("Build did not complete after generation lock release")
	}
}

func TestConcurrentBuildsCreateDistinctGenerations(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	createStoreEntry(t, storeRoot, "jq", "1.8.1", []string{"jq"})
	createStoreEntry(t, storeRoot, "fd", "10.4.2", []string{"fd"})

	errCh := make(chan error, 2)
	go func() {
		errCh <- Build(map[string]string{"jq": "1.8.1"}, galeDir, storeRoot)
	}()
	go func() {
		errCh <- Build(map[string]string{"jq": "1.8.1", "fd": "10.4.2"}, galeDir, storeRoot)
	}()

	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("concurrent Build error: %v", err)
		}
	}

	for _, gen := range []string{"1", "2"} {
		if _, err := os.Stat(filepath.Join(galeDir, "gen", gen)); err != nil {
			t.Fatalf("generation %s missing: %v", gen, err)
		}
	}

	current, err := os.Readlink(filepath.Join(galeDir, "current"))
	if err != nil {
		t.Fatalf("read current symlink: %v", err)
	}
	if current != filepath.Join("gen", "1") && current != filepath.Join("gen", "2") {
		t.Fatalf("current symlink = %q, want gen/1 or gen/2", current)
	}
}

func TestBuildDoesNotClobberConcurrentTempLink(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	createStoreEntry(t, storeRoot, "jq", "1.8.1", []string{"jq"})

	// Simulate another process's temp link already
	// existing. Build must not remove it.
	otherTmp := filepath.Join(galeDir, "current-new")
	if err := os.Symlink("gen/999", otherTmp); err != nil {
		t.Fatal(err)
	}

	pkgs := map[string]string{"jq": "1.8.1"}
	if err := Build(pkgs, galeDir, storeRoot); err != nil {
		t.Fatalf("Build error: %v", err)
	}

	// The other process's temp link should still exist.
	if _, err := os.Lstat(otherTmp); err != nil {
		t.Errorf("Build clobbered another process's temp link: %v", err)
	}
}

// --- Behavior 12: swapCurrentSymlink helper ---

func TestSwapCurrentSymlink(t *testing.T) {
	galeDir := t.TempDir()

	// Create gen/1 directory.
	gen1 := filepath.Join(galeDir, "gen", "1")
	if err := os.MkdirAll(gen1, 0o755); err != nil {
		t.Fatal(err)
	}

	// Swap to gen 1.
	if err := swapCurrentSymlink(galeDir, 1); err != nil {
		t.Fatalf("swapCurrentSymlink(1): %v", err)
	}

	// current should be a relative symlink to gen/1.
	currentPath := filepath.Join(galeDir, "current")
	target, err := os.Readlink(currentPath)
	if err != nil {
		t.Fatalf("readlink current: %v", err)
	}
	if target != filepath.Join("gen", "1") {
		t.Errorf("current = %q, want %q", target,
			filepath.Join("gen", "1"))
	}

	// Create gen/2 and swap again.
	gen2 := filepath.Join(galeDir, "gen", "2")
	if err := os.MkdirAll(gen2, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := swapCurrentSymlink(galeDir, 2); err != nil {
		t.Fatalf("swapCurrentSymlink(2): %v", err)
	}

	target, err = os.Readlink(currentPath)
	if err != nil {
		t.Fatalf("readlink current after swap: %v", err)
	}
	if target != filepath.Join("gen", "2") {
		t.Errorf("current = %q, want %q", target,
			filepath.Join("gen", "2"))
	}
}

func TestSwapCurrentSymlinkPreservesPIDScopedTempName(t *testing.T) {
	galeDir := t.TempDir()

	gen1 := filepath.Join(galeDir, "gen", "1")
	if err := os.MkdirAll(gen1, 0o755); err != nil {
		t.Fatal(err)
	}

	// Simulate another process's temp link (no PID suffix).
	otherTmp := filepath.Join(galeDir, "current-new")
	if err := os.Symlink("gen/999", otherTmp); err != nil {
		t.Fatal(err)
	}

	if err := swapCurrentSymlink(galeDir, 1); err != nil {
		t.Fatalf("swapCurrentSymlink: %v", err)
	}

	// The other process's temp link should still exist.
	if _, err := os.Lstat(otherTmp); err != nil {
		t.Errorf("swapCurrentSymlink clobbered another process's temp link: %v", err)
	}
}

// --- Behavior: populateGeneration creates symlinks ---

func TestPopulateGenerationCreatesSymlinks(t *testing.T) {
	storeRoot := t.TempDir()
	genDir := t.TempDir()

	// Create bin/ in the gen dir (Build always does this
	// before calling populateGeneration).
	if err := os.MkdirAll(
		filepath.Join(genDir, "bin"), 0o755,
	); err != nil {
		t.Fatal(err)
	}

	createStoreEntry(t, storeRoot, "jq", "1.8.1", []string{"jq"})
	createStoreEntry(t, storeRoot, "fd", "10.4.2", []string{"fd"})

	pkgs := map[string]string{
		"jq": "1.8.1",
		"fd": "10.4.2",
	}

	if err := populateGeneration(genDir, pkgs, storeRoot, nil); err != nil {
		t.Fatalf("populateGeneration error: %v", err)
	}

	// Verify symlinks exist for each executable.
	for _, exe := range []string{"jq", "fd"} {
		linkPath := filepath.Join(genDir, "bin", exe)
		info, err := os.Lstat(linkPath)
		if err != nil {
			t.Errorf("symlink %q does not exist: %v", exe, err)
			continue
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("expected %q to be a symlink", linkPath)
		}
	}
}

// newGenDir returns a temp generation dir with bin/ already
// created, the state build() hands populateGeneration.
func newGenDir(t *testing.T) string {
	t.Helper()
	genDir := t.TempDir()
	if err := os.MkdirAll(
		filepath.Join(genDir, "bin"), 0o755,
	); err != nil {
		t.Fatal(err)
	}
	return genDir
}

// TestPopulateGenerationFailsOnBinCollision replaces
// TestPopulateGenerationAlphabeticalConflictResolution, which pinned
// the gh#190 bug: alphabetical order silently decided which package
// owned a shared basename. Every collision is now reported at once,
// naming both providers.
func TestPopulateGenerationFailsOnBinCollision(t *testing.T) {
	storeRoot := t.TempDir()
	genDir := newGenDir(t)

	createStoreEntry(t, storeRoot, "alpha", "1.0", []string{"tool", "cut"})
	createStoreEntry(t, storeRoot, "beta", "1.0", []string{"tool"})
	createStoreEntry(t, storeRoot, "gamma", "1.0", []string{"cut"})

	pkgs := map[string]string{"alpha": "1.0", "beta": "1.0", "gamma": "1.0"}

	err := populateGeneration(genDir, pkgs, storeRoot, nil)
	var collErr *BinCollisionError
	if !errors.As(err, &collErr) {
		t.Fatalf("error = %v (%T), want *BinCollisionError", err, err)
	}
	// Both collisions in one error: a user with two of them fixes
	// them in one pass, not two sync cycles.
	want := []BinCollision{
		{Bin: "cut", Existing: "alpha", Incoming: "gamma"},
		{Bin: "tool", Existing: "alpha", Incoming: "beta"},
	}
	if !slices.Equal(collErr.Collisions, want) {
		t.Errorf("collisions = %+v, want %+v", collErr.Collisions, want)
	}
	for _, fragment := range []string{
		"tool", "cut", "alpha", "beta", "gamma", "[bin]",
	} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("error %q omits %q", err, fragment)
		}
	}
}

// TestPopulateGenerationHonorsBinOverride covers the escape hatch:
// the named package wins, the other provider's entry is left out,
// and no collision is reported.
func TestPopulateGenerationHonorsBinOverride(t *testing.T) {
	storeRoot := t.TempDir()
	genDir := newGenDir(t)

	createStoreEntry(t, storeRoot, "alpha", "1.0", []string{"tool"})
	createStoreEntry(t, storeRoot, "beta", "1.0", []string{"tool"})

	pkgs := map[string]string{"alpha": "1.0", "beta": "1.0"}
	overrides := map[string]string{"tool": "beta"}

	if err := populateGeneration(genDir, pkgs, storeRoot, overrides); err != nil {
		t.Fatalf("populateGeneration error: %v", err)
	}

	target, err := os.Readlink(filepath.Join(genDir, "bin", "tool"))
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if !strings.Contains(target, string(filepath.Separator)+"beta"+
		string(filepath.Separator)) {
		t.Errorf("bin/tool -> %s, want beta's copy", target)
	}
}

// TestPopulateGenerationAllowsNonBinCollisions scopes the refusal.
// Only bin/ decides what runs from PATH; a man page or a dylib
// shared by two packages keeps the long-standing skip-if-present
// behavior, because failing there would refuse generations that
// have always been correct.
func TestPopulateGenerationAllowsNonBinCollisions(t *testing.T) {
	storeRoot := t.TempDir()
	genDir := newGenDir(t)

	for _, name := range []string{"alpha", "beta"} {
		createStoreEntry(t, storeRoot, name, "1.0", []string{name})
		manDir := filepath.Join(storeRoot, name, "1.0", "man", "man1")
		if err := os.MkdirAll(manDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(manDir, "shared.1"), []byte(name), 0o644,
		); err != nil {
			t.Fatal(err)
		}
	}

	pkgs := map[string]string{"alpha": "1.0", "beta": "1.0"}

	if err := populateGeneration(genDir, pkgs, storeRoot, nil); err != nil {
		t.Fatalf("populateGeneration error: %v", err)
	}
	if _, err := os.Lstat(
		filepath.Join(genDir, "man", "man1", "shared.1"),
	); err != nil {
		t.Errorf("shared man page not linked: %v", err)
	}
}

func TestPopulateGenerationRootLevelFiles(t *testing.T) {
	storeRoot := t.TempDir()
	genDir := t.TempDir()

	if err := os.MkdirAll(
		filepath.Join(genDir, "bin"), 0o755,
	); err != nil {
		t.Fatal(err)
	}

	// Create package with root-level file.
	pkgDir := filepath.Join(storeRoot, "go", "1.26.1")
	if err := os.MkdirAll(
		filepath.Join(pkgDir, "bin"), 0o755,
	); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(pkgDir, "bin", "go"),
		[]byte("fake"), 0o755)
	os.WriteFile(filepath.Join(pkgDir, "go.env"),
		[]byte("GOPROXY=direct\n"), 0o644)

	pkgs := map[string]string{"go": "1.26.1"}

	if err := populateGeneration(genDir, pkgs, storeRoot, nil); err != nil {
		t.Fatalf("populateGeneration error: %v", err)
	}

	info, err := os.Lstat(filepath.Join(genDir, "go.env"))
	if err != nil {
		t.Fatalf("root file go.env not symlinked: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("expected go.env to be a symlink")
	}
}

// --- Behavior 13: Rollback uses unique temp-link path ---

func TestRollbackDoesNotClobberConcurrentTempLink(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := filepath.Join(galeDir, "pkg")

	createStoreEntry(t, storeRoot, "jq", "1.8.1", []string{"jq"})
	createStoreEntry(t, storeRoot, "fd", "10.4.2", []string{"fd"})

	if err := Build(map[string]string{"jq": "1.8.1"},
		galeDir, storeRoot); err != nil {
		t.Fatal(err)
	}
	if err := Build(map[string]string{"jq": "1.8.1", "fd": "10.4.2"},
		galeDir, storeRoot); err != nil {
		t.Fatal(err)
	}

	// Simulate another process's temp link.
	otherTmp := filepath.Join(galeDir, "current-new")
	if err := os.Symlink("gen/999", otherTmp); err != nil {
		t.Fatal(err)
	}

	if err := Rollback(galeDir, storeRoot, 1); err != nil {
		t.Fatalf("Rollback error: %v", err)
	}

	if _, err := os.Lstat(otherTmp); err != nil {
		t.Errorf("Rollback clobbered another process's temp link: %v", err)
	}
}

// TestCurrentVersionsReadsActiveGeneration confirms the
// exported helper returns the (name → version) map of the
// active generation. Sync uses this to detect when gale.toml
// has dropped a package that's still active in current/bin.
func TestCurrentVersionsReadsActiveGeneration(t *testing.T) {
	storeRoot := t.TempDir()
	galeDir := filepath.Join(t.TempDir(), ".gale")

	createStoreEntry(t, storeRoot, "alpha", "1.0", []string{"alpha"})
	createStoreEntry(t, storeRoot, "beta", "2.0", []string{"beta"})

	pkgs := map[string]string{"alpha": "1.0", "beta": "2.0"}
	if err := Build(pkgs, galeDir, storeRoot); err != nil {
		t.Fatalf("Build: %v", err)
	}

	got, err := CurrentVersions(galeDir, storeRoot)
	if err != nil {
		t.Fatalf("CurrentVersions: %v", err)
	}
	if got["alpha"] != "1.0" || got["beta"] != "2.0" {
		t.Errorf("CurrentVersions = %v, want alpha=1.0, beta=2.0", got)
	}
}

// TestCurrentVersionsStrictReportsUnreadableGenerationDir pins
// gh#210: a generation directory that cannot be walked is not an
// empty generation. The lenient reader keeps swallowing the walk
// error — Build's carry-forward and the history readers depend on
// that — while the strict sibling names the directory it could not
// read, so a caller deciding whether to destroy bytes can tell
// "references nothing" from "could not look".
//
// The failure is induced structurally, not with permission bits:
// CI and the agent container run tests as root and bypass a chmod,
// so a permission fixture passes for the wrong reason. Replacing
// the gen tree with a regular file makes the walk's root Lstat
// fail with ENOTDIR while current still readlinks to "gen/1" —
// Current parses the basename and never stats the directory.
func TestCurrentVersionsStrictReportsUnreadableGenerationDir(t *testing.T) {
	storeRoot := t.TempDir()
	galeDir := filepath.Join(t.TempDir(), ".gale")

	createStoreEntry(t, storeRoot, "alpha", "1.0", []string{"alpha"})
	if err := Build(
		map[string]string{"alpha": "1.0"}, galeDir, storeRoot,
	); err != nil {
		t.Fatalf("Build: %v", err)
	}

	genRoot := filepath.Join(galeDir, "gen")
	if err := os.RemoveAll(genRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		genRoot, []byte("not a directory"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Join("gen", "1"), filepath.Join(galeDir, "current"),
	); err != nil && !errors.Is(err, os.ErrExist) {
		t.Fatal(err)
	}

	lenient, err := CurrentVersions(galeDir, storeRoot)
	if err != nil {
		t.Fatalf("CurrentVersions must stay lenient: %v", err)
	}
	if len(lenient) != 0 {
		t.Errorf("lenient CurrentVersions = %v, want empty", lenient)
	}

	got, err := CurrentVersionsStrict(galeDir, storeRoot)
	if err == nil {
		t.Fatalf("CurrentVersionsStrict must report an unreadable "+
			"generation, got %v", got)
	}
	if want := filepath.Join("gen", "1"); !strings.Contains(
		err.Error(), want,
	) {
		t.Errorf("error must name %s, got: %v", want, err)
	}
}

// TestCurrentVersionsStrictReadsActiveGeneration pins the other
// half of gh#210: strictness must not cost the answer. A readable
// generation yields the same map the lenient reader returns, and
// no error.
func TestCurrentVersionsStrictReadsActiveGeneration(t *testing.T) {
	storeRoot := t.TempDir()
	galeDir := filepath.Join(t.TempDir(), ".gale")

	createStoreEntry(t, storeRoot, "alpha", "1.0", []string{"alpha"})
	createStoreEntry(t, storeRoot, "beta", "2.0", []string{"beta"})

	pkgs := map[string]string{"alpha": "1.0", "beta": "2.0"}
	if err := Build(pkgs, galeDir, storeRoot); err != nil {
		t.Fatalf("Build: %v", err)
	}

	got, err := CurrentVersionsStrict(galeDir, storeRoot)
	if err != nil {
		t.Fatalf("CurrentVersionsStrict: %v", err)
	}
	if got["alpha"] != "1.0" || got["beta"] != "2.0" {
		t.Errorf("CurrentVersionsStrict = %v, want alpha=1.0, beta=2.0",
			got)
	}
}

// TestCurrentVersionsStrictReturnsEmptyWhenNoGeneration keeps the
// fresh-install case out of the strict path's refusals: a scope
// that has never built a generation genuinely references nothing.
func TestCurrentVersionsStrictReturnsEmptyWhenNoGeneration(t *testing.T) {
	got, err := CurrentVersionsStrict(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatalf("CurrentVersionsStrict: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("CurrentVersionsStrict = %v, want empty", got)
	}
}

// TestCurrentVersionsReturnsEmptyWhenNoGeneration covers the
// fresh-install case where no current symlink exists yet.
func TestCurrentVersionsReturnsEmptyWhenNoGeneration(t *testing.T) {
	got, err := CurrentVersions(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatalf("CurrentVersions: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("CurrentVersions = %v, want empty", got)
	}
}

// TestBuildResolvesBareDevVersionToHighestRevision pins the
// contract that a version with embedded dashes from a dev tag
// (e.g. "0.16.2-dev.70+676b646") is still treated as "bare" —
// resolveStoreDir must scan for the highest "<v>-<N>" on disk
// and return that. The previous heuristic checked for ANY dash
// in the version string, which incorrectly classified dev
// versions as already-revisioned and forced an exact-match
// lookup that always missed.
//
// This is the failure mode that bit the gen/308 follow-up
// install: the install pipeline writes the bare dev version to
// gale.toml ("0.16.2-dev.70+676b646") with no -1 suffix, while
// the store holds the canonical revision form
// ("0.16.2-dev.70+676b646-1"). Without this fix, every
// subsequent rebuild errors out with "missing from the store".
func TestBuildResolvesBareDevVersionToHighestRevision(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	createStoreEntry(t, storeRoot, "gale",
		"0.16.2-dev.70+676b646-1", []string{"gale"})

	pkgs := map[string]string{"gale": "0.16.2-dev.70+676b646"}
	if err := Build(pkgs, galeDir, storeRoot); err != nil {
		t.Fatalf("Build: %v", err)
	}

	target, err := os.Readlink(
		filepath.Join(galeDir, "gen", "1", "bin", "gale"),
	)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	wantFragment := filepath.Join(
		"gale", "0.16.2-dev.70+676b646-1", "bin", "gale",
	)
	if !strings.Contains(target, wantFragment) {
		t.Errorf("gale symlink target = %q, want fragment %q",
			target, wantFragment)
	}
}

// TestBuildNeverMergesIntoAStaleGenDir is a regression test for
// the secondary corruption observed during the gen/308 incident:
// when Build's target gen number already existed, populateGeneration
// merged into the leftover dir because symlinkDir skips
// destinations that already have a symlink. Result: the new
// gen ships stale symlinks from the old attempt and
// validateGenerationSymlinks doesn't catch them because the
// stale targets still resolve.
//
// The invariant: a new generation reflects the declared pkgs map
// exactly, never a merge with a leftover.
//
// gh#189 moved the number this lands on. Build now allocates above
// the highest generation directory, so a leftover gen/1 is stepped
// over rather than reused — and the stale dir it names survives,
// because a generation number identifies one snapshot for good.
// The merge this test guards is unreachable from that direction;
// TestBuildClearsNonDirectoryLeftoverAtTargetNumber covers the one
// that remains.
func TestBuildNeverMergesIntoAStaleGenDir(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	createStoreEntry(t, storeRoot, "jq", "1.0", []string{"jq"})
	createStoreEntry(t, storeRoot, "jq", "2.0", []string{"jq"})
	createStoreEntry(t, storeRoot, "obsolete", "1.0", []string{"obsolete"})

	// Plant a stale gen/1 simulating a prior bad Build:
	//   - jq points at the OLD revision (1.0), not the 2.0 we'll
	//     declare next
	//   - "obsolete" is for a package not in the new pkgs map
	// Both targets are real store dirs so validateGenerationSymlinks
	// wouldn't catch the staleness on its own — the swap would
	// otherwise succeed and the user gets a corrupt gen.
	staleBin := filepath.Join(galeDir, "gen", "1", "bin")
	if err := os.MkdirAll(staleBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Join(storeRoot, "jq", "1.0", "bin", "jq"),
		filepath.Join(staleBin, "jq"),
	); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Join(storeRoot, "obsolete", "1.0", "bin", "obsolete"),
		filepath.Join(staleBin, "obsolete"),
	); err != nil {
		t.Fatal(err)
	}

	// There is no current symlink, but gen/1 exists, so Build
	// allocates gen/2 and leaves the stale snapshot alone.
	pkgs := map[string]string{"jq": "2.0"}
	if err := Build(pkgs, galeDir, storeRoot); err != nil {
		t.Fatalf("Build: %v", err)
	}

	cur, err := Current(galeDir)
	if err != nil {
		t.Fatalf("read current: %v", err)
	}
	if cur != 2 {
		t.Fatalf("current = %d, want 2 — Build must allocate above "+
			"the highest generation dir, not reuse gen/1", cur)
	}

	newBin := filepath.Join(galeDir, "gen", "2", "bin")
	target, err := os.Readlink(filepath.Join(newBin, "jq"))
	if err != nil {
		t.Fatalf("readlink jq: %v", err)
	}
	wantFragment := filepath.Join("jq", "2.0")
	if !strings.Contains(target, wantFragment) {
		t.Errorf("jq symlink points at stale store dir: got %q, want target containing %q",
			target, wantFragment)
	}

	if _, err := os.Lstat(filepath.Join(newBin, "obsolete")); !os.IsNotExist(err) {
		t.Errorf("obsolete symlink should not survive rebuild, err=%v", err)
	}

	// The stale gen/1 is history now, not scratch space: its
	// symlinks stay exactly as planted.
	staleTarget, err := os.Readlink(filepath.Join(staleBin, "jq"))
	if err != nil {
		t.Fatalf("readlink stale jq: %v", err)
	}
	if !strings.Contains(staleTarget, filepath.Join("jq", "1.0")) {
		t.Errorf("gen/1/bin/jq = %q, want the planted 1.0 target — "+
			"Build must not touch a generation it did not allocate",
			staleTarget)
	}
	if _, err := os.Lstat(filepath.Join(staleBin, "obsolete")); err != nil {
		t.Errorf("gen/1/bin/obsolete must survive: %v", err)
	}
}

// TestBuildClearsNonDirectoryLeftoverAtTargetNumber guards the
// teardown that survives gh#189. Allocating above the highest
// generation directory rules out landing on a leftover *directory*,
// but the scan skips entries that are not directories, so a regular
// file (or any other non-directory entry) can still sit at
// gen/<next>. os.MkdirAll would fail with ENOTDIR on it.
//
// Build must clear whatever is at the target number before
// populating.
func TestBuildClearsNonDirectoryLeftoverAtTargetNumber(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	createStoreEntry(t, storeRoot, "jq", "2.0", []string{"jq"})

	pkgs := map[string]string{"jq": "2.0"}
	if err := Build(pkgs, galeDir, storeRoot); err != nil {
		t.Fatalf("Build gen 1: %v", err)
	}

	// A regular file where the next generation dir belongs.
	if err := os.WriteFile(
		filepath.Join(galeDir, "gen", "2"), []byte("leftover"), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	if err := Build(pkgs, galeDir, storeRoot); err != nil {
		t.Fatalf("Build gen 2 over a non-directory leftover: %v", err)
	}

	target, err := os.Readlink(
		filepath.Join(galeDir, "gen", "2", "bin", "jq"),
	)
	if err != nil {
		t.Fatalf("readlink gen/2/bin/jq: %v", err)
	}
	wantFragment := filepath.Join("jq", "2.0")
	if !strings.Contains(target, wantFragment) {
		t.Errorf("gen/2/bin/jq = %q, want target containing %q",
			target, wantFragment)
	}
}

// TestBuildSymlinksAllDeclaredPackages is a regression test for
// an observed production failure: a user with 44 declared
// packages ended up with only ~23 binaries in current/bin after
// `just install`. Each package had a unique single-binary store
// entry; the generation builder should symlink every one.
//
// This is a unit test against generation.Build with no install
// pipeline. If it passes, the bug is upstream (something
// constructs an incomplete pkgs map). If it fails, the bug is
// here.
func TestBuildSymlinksAllDeclaredPackages(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	// 20 packages, one binary each, names chosen to span the
	// alphabet like a real gale.toml.
	pkgs := map[string]string{
		"atuin":    "1.0",
		"autossh":  "1.0",
		"btop":     "1.0",
		"chezmoi":  "1.0",
		"curl":     "1.0",
		"direnv":   "1.0",
		"fish":     "1.0",
		"fzf":      "1.0",
		"gale":     "1.0",
		"gh":       "1.0",
		"git":      "1.0",
		"glow":     "1.0",
		"jq":       "1.0",
		"just":     "1.0",
		"lazygit":  "1.0",
		"mise":     "1.0",
		"neovim":   "1.0",
		"ripgrep":  "1.0",
		"starship": "1.0",
		"zmx":      "1.0",
	}
	for name, version := range pkgs {
		createStoreEntry(t, storeRoot, name, version, []string{name})
	}

	if err := Build(pkgs, galeDir, storeRoot); err != nil {
		t.Fatalf("Build error: %v", err)
	}

	genBinDir := filepath.Join(galeDir, "gen", "1", "bin")
	entries, err := os.ReadDir(genBinDir)
	if err != nil {
		t.Fatalf("read gen bin: %v", err)
	}

	got := make(map[string]bool, len(entries))
	for _, e := range entries {
		got[e.Name()] = true
	}

	for name := range pkgs {
		if !got[name] {
			t.Errorf("gen/1/bin missing symlink for %q (have %d/%d binaries)",
				name, len(got), len(pkgs))
		}
	}
	if len(got) != len(pkgs) {
		t.Errorf("gen/1/bin has %d entries, want %d", len(got), len(pkgs))
	}
}

// TestBuildMultiRevisionPicksHighest models the regression in
// gen/308: store layout has packages with multiple coexisting
// revisions ("<version>-1" alongside "<version>-6"), AND packages
// with a single non-1 revision ("<version>-2" only). gale.toml
// declares the BARE version. Build must:
//   - link to the HIGHEST revision when multiples exist, NOT -1
//   - include the package when only a non-1 revision exists
//
// In production gen/308: glib's symlinks went to 2.88.1-1 even
// though 2.88.1-6 existed; atuin@18.13.6 was skipped entirely
// even though 18.13.6-2 existed. This test would fail with
// either of those misbehaviours.
func TestBuildMultiRevisionPicksHighest(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	// glib has BOTH 2.88.1-1 (old, like a May-5 install) AND
	// 2.88.1-6 (new, like a streaming-extract install).
	// Each has its own binary file with the rev in the content
	// so we can tell which one the symlink resolves to.
	mkRev := func(name, version string, rev int) {
		t.Helper()
		dir := filepath.Join(storeRoot, name, version+"-"+strconv.Itoa(rev), "bin")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		path := filepath.Join(dir, name)
		content := []byte("rev=" + strconv.Itoa(rev))
		if err := os.WriteFile(path, content, 0o755); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	mkRev("glib", "2.88.1", 1)
	mkRev("glib", "2.88.1", 6)
	// atuin has ONLY 18.13.6-2 — no -1 sibling.
	mkRev("atuin", "18.13.6", 2)
	// zmx has -1 AND -2 — pick -2.
	mkRev("zmx", "0.6.0", 1)
	mkRev("zmx", "0.6.0", 2)

	pkgs := map[string]string{
		"glib":  "2.88.1",
		"atuin": "18.13.6",
		"zmx":   "0.6.0",
	}

	if err := Build(pkgs, galeDir, storeRoot); err != nil {
		t.Fatalf("Build: %v", err)
	}

	cases := []struct {
		name    string
		wantRev int
	}{
		{"glib", 6},
		{"atuin", 2},
		{"zmx", 2},
	}
	for _, c := range cases {
		linkPath := filepath.Join(galeDir, "gen", "1", "bin", c.name)
		target, err := os.Readlink(linkPath)
		if err != nil {
			t.Errorf("readlink %s: %v", c.name, err)
			continue
		}
		// The target should point into <storeRoot>/<name>/<version>-<wantRev>/bin/<name>.
		wantSuffix := filepath.Join(c.name, pkgs[c.name]+"-"+strconv.Itoa(c.wantRev), "bin", c.name)
		if !strings.HasSuffix(target, wantSuffix) {
			t.Errorf("%s symlink → %s, want suffix %s",
				c.name, target, wantSuffix)
		}
		// And the resolved file content carries the right rev.
		data, err := os.ReadFile(linkPath)
		if err != nil {
			t.Errorf("read through symlink for %s: %v", c.name, err)
			continue
		}
		if string(data) != "rev="+strconv.Itoa(c.wantRev) {
			t.Errorf("%s content = %q, want rev=%d",
				c.name, string(data), c.wantRev)
		}
	}
}

// TestPruneOldGenerationsKeepsLastN pins the auto-gc retention
// policy: gen dirs with number strictly less than (curGen-keep+1)
// are removed, anything >= that threshold (including curGen and
// in-flight gen/curGen+1) is preserved. Returns the removed gen
// numbers in ascending order so the caller can print them.
func TestPruneOldGenerationsKeepsLastN(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()
	if err := os.MkdirAll(storeRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	// Stage gens 1..15 (just empty dirs — Prune doesn't read
	// content). current → gen/15.
	genRoot := filepath.Join(galeDir, "gen")
	for i := 1; i <= 15; i++ {
		if err := os.MkdirAll(filepath.Join(genRoot, strconv.Itoa(i), "bin"), 0o755); err != nil {
			t.Fatalf("stage gen/%d: %v", i, err)
		}
	}
	if err := os.Symlink(filepath.Join("gen", "15"),
		filepath.Join(galeDir, "current")); err != nil {
		t.Fatalf("link current: %v", err)
	}

	removed, err := PruneOldGenerations(galeDir, storeRoot, 10)
	if err != nil {
		t.Fatalf("PruneOldGenerations: %v", err)
	}

	wantRemoved := []int{1, 2, 3, 4, 5}
	if len(removed) != len(wantRemoved) {
		t.Fatalf("removed = %v, want %v", removed, wantRemoved)
	}
	for i, n := range wantRemoved {
		if removed[i] != n {
			t.Errorf("removed[%d] = %d, want %d", i, removed[i], n)
		}
	}

	// On disk: 1-5 gone, 6-15 still present, current still gen/15.
	for i := 1; i <= 5; i++ {
		if _, err := os.Stat(filepath.Join(genRoot, strconv.Itoa(i))); !os.IsNotExist(err) {
			t.Errorf("gen/%d should have been removed (err=%v)", i, err)
		}
	}
	for i := 6; i <= 15; i++ {
		if _, err := os.Stat(filepath.Join(genRoot, strconv.Itoa(i))); err != nil {
			t.Errorf("gen/%d should be preserved: %v", i, err)
		}
	}
}

// TestPruneOldGenerationsNoOpUnderThreshold: when total gens
// is <= keep, nothing is removed.
func TestPruneOldGenerationsNoOpUnderThreshold(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()
	if err := os.MkdirAll(storeRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	for i := 1; i <= 5; i++ {
		if err := os.MkdirAll(filepath.Join(galeDir, "gen", strconv.Itoa(i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join("gen", "5"),
		filepath.Join(galeDir, "current")); err != nil {
		t.Fatal(err)
	}

	removed, err := PruneOldGenerations(galeDir, storeRoot, 10)
	if err != nil {
		t.Fatalf("PruneOldGenerations: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("removed = %v, want no removals when under threshold", removed)
	}
}

// TestPruneOldGenerationsNoCurrentIsNoop: with no current
// symlink, there's no defined "newest gen" to anchor against —
// be conservative and do nothing.
func TestPruneOldGenerationsNoCurrentIsNoop(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()
	if err := os.MkdirAll(storeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(galeDir, "gen", "1"), 0o755); err != nil {
		t.Fatal(err)
	}

	removed, err := PruneOldGenerations(galeDir, storeRoot, 1)
	if err != nil {
		t.Fatalf("PruneOldGenerations: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("removed = %v, want no removals when no current symlink", removed)
	}
	if _, err := os.Stat(filepath.Join(galeDir, "gen", "1")); err != nil {
		t.Errorf("gen/1 should be preserved: %v", err)
	}
}

// TestPruneOldGenerationsKeepZeroIsNoop: keep<=0 means "the
// caller didn't ask for auto-gc"; do nothing rather than wipe.
func TestPruneOldGenerationsKeepZeroIsNoop(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()
	if err := os.MkdirAll(storeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		if err := os.MkdirAll(filepath.Join(galeDir, "gen", strconv.Itoa(i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join("gen", "5"),
		filepath.Join(galeDir, "current")); err != nil {
		t.Fatal(err)
	}

	removed, err := PruneOldGenerations(galeDir, storeRoot, 0)
	if err != nil {
		t.Fatalf("PruneOldGenerations: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("keep=0 should remove nothing, got %v", removed)
	}
}

// TestPruneOldGenerationsCountsPositionally pins retention as a
// COUNT over the generations at or below current, not the numeric
// cutoff curGen-keep+1 (gh#248). The two agree only while the
// numbering is contiguous, and since gh#189 allocation is
// max(prev, highest)+1, so gaps are ordinary.
//
// Above current nothing changes: that branch is retained history a
// roll-forward may return to, reclaimed only by naming it
// (gh#189, gh#206). The count therefore spans {n <= current}, and
// current itself is one of the keep.
func TestPruneOldGenerationsCountsPositionally(t *testing.T) {
	cases := []struct {
		name string
		nums []int
		cur  int
		keep int
		left []int
	}{{
		// The cutoff reads 8 here and keeps only 9 and 10 — two
		// generations where keep promised three. This is the
		// destructive direction: gen/5 is a rollback target the
		// user could see, and auto-gc deletes it.
		name: "gaps below current",
		nums: []int{1, 5, 9, 10},
		cur:  10, keep: 3,
		left: []int{5, 9, 10},
	}, {
		// After a rollback current sits below the highest
		// generation. Three at or below current (3, 4, 5) plus
		// the abandoned branch 6..10, which prune never touches.
		name: "current below the highest generation",
		nums: []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
		cur:  5, keep: 3,
		left: []int{3, 4, 5, 6, 7, 8, 9, 10},
	}, {
		// Contiguous: the count and the cutoff agree, and the
		// answer must not move.
		name: "contiguous numbering is unchanged",
		nums: []int{1, 2, 3, 4, 5},
		cur:  5, keep: 2,
		left: []int{4, 5},
	}, {
		// A current above every staged generation — the shape a
		// removed-then-rebuilt history leaves — put the cutoff
		// past all of them and swept the lot.
		name: "current above every staged generation",
		nums: []int{1, 2, 3},
		cur:  10, keep: 3,
		left: []int{1, 2, 3},
	}, {
		name: "no current symlink",
		nums: []int{1, 2, 3},
		cur:  0, keep: 1,
		left: []int{1, 2, 3},
	}, {
		name: "keep zero",
		nums: []int{1, 2, 3},
		cur:  3, keep: 0,
		left: []int{1, 2, 3},
	}, {
		name: "keep negative",
		nums: []int{1, 2, 3},
		cur:  3, keep: -1,
		left: []int{1, 2, 3},
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			galeDir := t.TempDir()
			storeRoot := t.TempDir()
			stageGenNumbers(t, galeDir, c.nums, c.cur)

			var wantRemoved []int
			for _, n := range c.nums {
				if !slices.Contains(c.left, n) {
					wantRemoved = append(wantRemoved, n)
				}
			}

			removed, err := PruneOldGenerations(galeDir, storeRoot, c.keep)
			if err != nil {
				t.Fatalf("PruneOldGenerations: %v", err)
			}
			if !slices.Equal(removed, wantRemoved) {
				t.Errorf("removed = %v, want %v", removed, wantRemoved)
			}

			onDisk, err := genNumbers(galeDir)
			if err != nil {
				t.Fatalf("genNumbers: %v", err)
			}
			if !slices.Equal(onDisk, c.left) {
				t.Errorf("generations left on disk = %v, want %v — "+
					"keep=%d promises the highest %d at or below "+
					"gen/%d, plus everything above it",
					onDisk, c.left, c.keep, c.keep, c.cur)
			}
		})
	}
}

// stageGens creates gen/1..n under galeDir and points current at
// cur. Prune and Remove read only the directory names and the
// current symlink, so empty dirs are a faithful fixture.
func stageGens(t *testing.T, galeDir string, n, cur int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		if err := os.MkdirAll(
			filepath.Join(galeDir, "gen", strconv.Itoa(i)), 0o755,
		); err != nil {
			t.Fatalf("stage gen/%d: %v", i, err)
		}
	}
	if err := os.Symlink(
		filepath.Join("gen", strconv.Itoa(cur)),
		filepath.Join(galeDir, "current"),
	); err != nil {
		t.Fatalf("link current: %v", err)
	}
}

// TestRemoveGenerationsRefusesCurrent pins the guard that keeps
// Remove from cutting the branch it stands on: removing the
// generation current points at would dangle the symlink, empty
// PATH, and fail doctor's generation check. Validation covers
// every target before anything is removed, so a batch naming
// current removes nothing at all.
func TestRemoveGenerationsRefusesCurrent(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := filepath.Join(galeDir, "pkg")
	if err := os.MkdirAll(storeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	stageGens(t, galeDir, 3, 2)

	removed, err := Remove(galeDir, storeRoot, []int{3, 2})
	if err == nil {
		t.Fatalf("Remove of current generation returned nil error, "+
			"removed %v", removed)
	}
	if !strings.Contains(err.Error(), "2") {
		t.Errorf("error = %q, want it to name generation 2", err)
	}
	if len(removed) != 0 {
		t.Errorf("removed = %v, want none — validation must cover "+
			"every target before the first removal", removed)
	}
	for i := 1; i <= 3; i++ {
		if _, sErr := os.Stat(
			filepath.Join(galeDir, "gen", strconv.Itoa(i)),
		); sErr != nil {
			t.Errorf("gen/%d must survive a refused Remove: %v", i, sErr)
		}
	}
}

// TestRemoveGenerationsSerializesWithBuild pins the lock
// contract: Remove destroys directories Build creates and swaps
// between, so it must take the same store-rooted generation lock
// Build, Rollback and PruneOldGenerations take. Holding that lock
// across Build's whole create-then-swap span is also what makes
// refusing `current` a sufficient in-flight guard — no half-built
// generation is visible to Remove while the lock is held.
func TestRemoveGenerationsSerializesWithBuild(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := filepath.Join(galeDir, "pkg")
	if err := os.MkdirAll(storeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	stageGens(t, galeDir, 3, 2)

	lockPath := filepath.Join(galeDir, "generation.lock")

	const holdDuration = 100 * time.Millisecond
	holdAcquired := make(chan struct{})
	holdDone := make(chan struct{})

	// Own the lock for holdDuration, then release. The timer
	// starts inside the lock so the hold is always bounded, even
	// if Remove never waits (the bug case).
	go func() {
		defer close(holdDone)
		_ = filelock.With(lockPath, func() error {
			close(holdAcquired)
			time.Sleep(holdDuration)
			return nil
		})
	}()
	<-holdAcquired

	start := time.Now()
	removed, err := Remove(galeDir, storeRoot, []int{3})
	waited := time.Since(start)
	<-holdDone

	if err != nil {
		t.Fatalf("Remove while gen lock held: %v", err)
	}
	if len(removed) != 1 || removed[0] != 3 {
		t.Errorf("removed = %v, want [3]", removed)
	}

	// Allow scheduling jitter below the hold time.
	const jitter = 20 * time.Millisecond
	if waited < holdDuration-jitter {
		t.Errorf("Remove returned in %v, want it to block at least "+
			"%v — it is not taking the store-rooted generation lock, "+
			"so it can delete a generation an in-flight Build is "+
			"populating", waited, holdDuration-jitter)
	}
}

// TestBuildSkipsNonFunctionalTopLevelDirs pins the inode budget
// for a gen: top-level dirs that no consumer reads through the
// gen path (src/, api/, pkg/, doc/, misc/) must not be mirrored.
// Packages still ship these in their store dir, and tools like
// Go's compiler read them from GOROOT — which resolves to the
// store path (via dirname twice on the resolved binary path),
// not the gen — so mirroring them was always dead weight.
//
// The motivating example: Go ships ~12K files under src/. That
// single dir was ~45% of a typical gen's inode count and was
// invisible to PATH. Skipping it (and the other dead dirs) is
// nearly two orders of magnitude inode reduction per gen.
//
// The functional dirs (bin/, lib/, share/, include/, libexec/,
// etc/) MUST still mirror per-file so multiple packages can
// contribute to a unified namespace and conflicts get caught
// by symlinkDir's first-pkg-wins.
func TestBuildSkipsNonFunctionalTopLevelDirs(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	pkgDir := filepath.Join(storeRoot, "go", "1.0.0")
	for _, sub := range []string{
		"bin", "lib", "share", "include",
		"libexec", "etc", "src", "api", "pkg", "doc", "misc",
	} {
		if err := os.MkdirAll(filepath.Join(pkgDir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
		if err := os.WriteFile(
			filepath.Join(pkgDir, sub, "marker"),
			[]byte("from "+sub), 0o644,
		); err != nil {
			t.Fatalf("write marker in %s: %v", sub, err)
		}
	}

	if err := Build(map[string]string{"go": "1.0.0"}, galeDir, storeRoot); err != nil {
		t.Fatalf("Build: %v", err)
	}

	gen := filepath.Join(galeDir, "gen", "1")

	for _, sub := range []string{"bin", "lib", "share", "include", "libexec", "etc"} {
		if _, err := os.Stat(filepath.Join(gen, sub, "marker")); err != nil {
			t.Errorf("functional dir %s missing from gen: %v", sub, err)
		}
	}

	for _, sub := range []string{"src", "api", "pkg", "doc", "misc"} {
		if _, err := os.Stat(filepath.Join(gen, sub)); err == nil {
			t.Errorf("skipped dir %s should not be mirrored into gen", sub)
		} else if !os.IsNotExist(err) {
			t.Errorf("unexpected stat error for %s: %v", sub, err)
		}
	}
}

// stageGenNumbers creates exactly the named generation
// directories under galeDir and points current at cur. Unlike
// stageGens it takes an explicit set, so a listing with gaps —
// legitimate since gh#189 made allocation max(prev,highest)+1 —
// can be staged directly. cur == 0 stages no current symlink.
func stageGenNumbers(t *testing.T, galeDir string, nums []int, cur int) {
	t.Helper()
	for _, n := range nums {
		if err := os.MkdirAll(
			filepath.Join(galeDir, "gen", strconv.Itoa(n)), 0o755,
		); err != nil {
			t.Fatalf("stage gen/%d: %v", n, err)
		}
	}
	if cur == 0 {
		return
	}
	if err := os.Symlink(
		filepath.Join("gen", strconv.Itoa(cur)),
		filepath.Join(galeDir, "current"),
	); err != nil {
		t.Fatalf("link current: %v", err)
	}
}

// TestRetainedNumbersKeepsCurrentAndTheBranchAboveIt pins the
// rule: the active generation plus every generation above it —
// the branch a rollback abandoned, which a roll-forward may
// return to (gh#189) — and nothing below.
//
// The set is the exact complement of what cleanOldGenerations
// removes (n < curGen), which is what makes a hollow generation
// impossible: gc never keeps a directory without the store dirs
// it links, and never retains bytes for a directory it deleted.
//
// Gaps in the numbering are legitimate and irrelevant here: the
// rule is a comparison against curGen, not a count.
func TestRetainedNumbersKeepsCurrentAndTheBranchAboveIt(t *testing.T) {
	galeDir := t.TempDir()
	stageGenNumbers(t, galeDir, []int{1, 5, 9, 10}, 5)

	got, err := retainedNumbers(galeDir)
	if err != nil {
		t.Fatalf("retainedNumbers: %v", err)
	}
	want := []int{5, 9, 10}
	if !slices.Equal(got, want) {
		t.Errorf("retainedNumbers(cur=5) = %v, want %v — current "+
			"and the branch above it, and gen/1 below it left to "+
			"cleanOldGenerations", got, want)
	}
}

// TestRetainedNumbersNoCurrentRetainsEverything pins the
// lost-current case: nothing is deleted, so nothing is swept.
// cleanOldGenerations' n >= 0 skip already kept every directory
// in that state; retention now agrees, which stops a missing
// current symlink from letting gc sweep the store bare.
func TestRetainedNumbersNoCurrentRetainsEverything(t *testing.T) {
	galeDir := t.TempDir()
	stageGenNumbers(t, galeDir, []int{1, 2, 3}, 0)

	got, err := retainedNumbers(galeDir)
	if err != nil {
		t.Fatalf("retainedNumbers: %v", err)
	}
	if want := []int{1, 2, 3}; !slices.Equal(got, want) {
		t.Errorf("retainedNumbers(no current) = %v, want %v", got, want)
	}
}

// TestRetainedNumbersIncludesAbsentCurrent pins the strictness
// hand-off. A numeric current pointing at a directory that is
// gone is not "a generation nobody listed": it must reach
// RetainedVersionsStrict as an unreadable generation so gc
// refuses to sweep on it, exactly as CurrentVersionsStrict did
// before (gh#188).
func TestRetainedNumbersIncludesAbsentCurrent(t *testing.T) {
	galeDir := t.TempDir()
	stageGenNumbers(t, galeDir, []int{1, 2}, 2)
	if err := os.RemoveAll(
		filepath.Join(galeDir, "gen", "2"),
	); err != nil {
		t.Fatal(err)
	}

	got, err := retainedNumbers(galeDir)
	if err != nil {
		t.Fatalf("retainedNumbers: %v", err)
	}
	if !slices.Contains(got, 2) {
		t.Errorf("retainedNumbers = %v, must include the active "+
			"generation 2 even though its directory is gone", got)
	}
}

// TestRetainedVersionsStrictUnionsRetainedGenerations pins the
// multi-version shape: the active generation and the branch
// above it can link two versions of the same package — the
// ordinary state after an upgrade then a rollback — and BOTH
// must be retained. A name → version map cannot hold that, which
// is why this reader answers with every version per name.
func TestRetainedVersionsStrictUnionsRetainedGenerations(t *testing.T) {
	storeRoot := t.TempDir()
	galeDir := filepath.Join(t.TempDir(), ".gale")

	createStoreEntry(t, storeRoot, "jq", "1.7", []string{"jq"})
	createStoreEntry(t, storeRoot, "jq", "1.8", []string{"jq"})
	if err := Build(
		map[string]string{"jq": "1.7"}, galeDir, storeRoot,
	); err != nil {
		t.Fatalf("Build gen 1: %v", err)
	}
	if err := Build(
		map[string]string{"jq": "1.8"}, galeDir, storeRoot,
	); err != nil {
		t.Fatalf("Build gen 2: %v", err)
	}
	if err := Rollback(galeDir, storeRoot, 1); err != nil {
		t.Fatalf("Rollback to gen 1: %v", err)
	}

	got, err := RetainedVersionsStrict(galeDir, storeRoot)
	if err != nil {
		t.Fatalf("RetainedVersionsStrict: %v", err)
	}
	versions := got["jq"]
	slices.Sort(versions)
	if want := []string{"1.7", "1.8"}; !slices.Equal(versions, want) {
		t.Errorf("RetainedVersionsStrict[jq] = %v, want %v — the "+
			"active generation and the branch above it both "+
			"contribute their closure", versions, want)
	}
}

// TestRetainedVersionsStrictSkipsHistoryBelowCurrent is the
// other half: a generation below current contributes nothing,
// because cleanOldGenerations is about to delete it. Retaining
// its closure would keep bytes alive for a directory that is
// gone — and break `gale update && gale gc` (gh#137).
func TestRetainedVersionsStrictSkipsHistoryBelowCurrent(t *testing.T) {
	storeRoot := t.TempDir()
	galeDir := filepath.Join(t.TempDir(), ".gale")

	createStoreEntry(t, storeRoot, "jq", "1.7", []string{"jq"})
	createStoreEntry(t, storeRoot, "jq", "1.8", []string{"jq"})
	if err := Build(
		map[string]string{"jq": "1.7"}, galeDir, storeRoot,
	); err != nil {
		t.Fatalf("Build gen 1: %v", err)
	}
	if err := Build(
		map[string]string{"jq": "1.8"}, galeDir, storeRoot,
	); err != nil {
		t.Fatalf("Build gen 2: %v", err)
	}

	got, err := RetainedVersionsStrict(galeDir, storeRoot)
	if err != nil {
		t.Fatalf("RetainedVersionsStrict: %v", err)
	}
	if want := []string{"1.8"}; !slices.Equal(got["jq"], want) {
		t.Errorf("RetainedVersionsStrict[jq] = %v, want %v — gen/1 "+
			"is below current and its closure is not retained",
			got["jq"], want)
	}
}

// TestRetainedVersionsStrictRefusesUnreadableRetainedGeneration
// pins gh#210 at the new reader: a retained generation that
// cannot be read is not a generation that references nothing.
// The error names the number so the user can act on it, and gc's
// refuse-to-sweep path (gh#188) turns it into a run that deletes
// nothing.
//
// The break is structural, not a chmod: CI and the agent
// container run as root and bypass permission bits. Here the
// active generation's directory is removed while current still
// points at it — precisely the state a partial crash leaves.
func TestRetainedVersionsStrictRefusesUnreadableRetainedGeneration(
	t *testing.T,
) {
	storeRoot := t.TempDir()
	galeDir := filepath.Join(t.TempDir(), ".gale")

	createStoreEntry(t, storeRoot, "jq", "1.7", []string{"jq"})
	if err := Build(
		map[string]string{"jq": "1.7"}, galeDir, storeRoot,
	); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if err := os.RemoveAll(
		filepath.Join(galeDir, "gen", "1"),
	); err != nil {
		t.Fatal(err)
	}

	got, err := RetainedVersionsStrict(galeDir, storeRoot)
	if err == nil {
		t.Fatalf("RetainedVersionsStrict must refuse an unreadable "+
			"retained generation, got %v", got)
	}
	for _, want := range []string{
		"generation 1", "gale generations remove 1", "--force",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must mention %q, got: %v", want, err)
		}
	}
}

// TestRetainedVersionsStrictRefusesUnreadableGenListing pins the
// other structural failure: the gen tree itself unreadable. The
// listing feeds the rule, so gc must refuse rather than compute
// retention from an empty set.
func TestRetainedVersionsStrictRefusesUnreadableGenListing(t *testing.T) {
	storeRoot := t.TempDir()
	galeDir := filepath.Join(t.TempDir(), ".gale")

	createStoreEntry(t, storeRoot, "jq", "1.7", []string{"jq"})
	if err := Build(
		map[string]string{"jq": "1.7"}, galeDir, storeRoot,
	); err != nil {
		t.Fatalf("Build: %v", err)
	}
	genRoot := filepath.Join(galeDir, "gen")
	if err := os.RemoveAll(genRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		genRoot, []byte("not a directory"), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	if got, err := RetainedVersionsStrict(
		galeDir, storeRoot,
	); err == nil {
		t.Fatalf("RetainedVersionsStrict must refuse an unreadable "+
			"gen listing, got %v", got)
	}
}
