package generation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

// --- Behavior 5: Next returns incremented generation number ---

func TestNextReturnsIncrementedNumber(t *testing.T) {
	galeDir := t.TempDir()

	// Set up current generation as 5.
	gensDir := filepath.Join(galeDir, "gen", "5", "bin")
	if err := os.MkdirAll(gensDir, 0o755); err != nil {
		t.Fatalf("failed to create gen dir: %v", err)
	}
	if err := os.Symlink(
		filepath.Join("gen", "5"),
		filepath.Join(galeDir, "current"),
	); err != nil {
		t.Fatalf("failed to create current symlink: %v", err)
	}

	got, err := Next(galeDir)
	if err != nil {
		t.Fatalf("Next error: %v", err)
	}
	if got != 6 {
		t.Errorf("Next = %d, want 6", got)
	}
}

func TestNextReturnsOneWhenNoCurrentExists(t *testing.T) {
	galeDir := t.TempDir()

	got, err := Next(galeDir)
	if err != nil {
		t.Fatalf("Next error: %v", err)
	}
	if got != 1 {
		t.Errorf("Next = %d, want 1", got)
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
		filepath.Join(pkgDir, "bin"), 0o755); err != nil {
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
			filepath.Join(pkgDir, sub), 0o755); err != nil {
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
			filepath.Join(pkgDir, sub), 0o755); err != nil {
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

// --- Behavior 10: Deterministic symlink conflict resolution ---

func TestBuildDeterministicSymlinkOrder(t *testing.T) {
	// Two packages provide the same binary "tool". With
	// sorted iteration, "alpha" (first alphabetically)
	// should always win.
	for i := 0; i < 20; i++ {
		galeDir := t.TempDir()
		storeRoot := t.TempDir()

		for _, name := range []string{"alpha", "beta"} {
			binDir := filepath.Join(
				storeRoot, name, "1.0", "bin")
			if err := os.MkdirAll(binDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(
				filepath.Join(binDir, "tool"),
				[]byte(name), 0o755); err != nil {
				t.Fatal(err)
			}
		}

		pkgs := map[string]string{
			"alpha": "1.0",
			"beta":  "1.0",
		}

		if err := Build(pkgs, galeDir, storeRoot); err != nil {
			t.Fatal(err)
		}

		toolLink := filepath.Join(
			galeDir, "current", "bin", "tool")
		target, err := os.Readlink(toolLink)
		if err != nil {
			t.Fatalf("iteration %d: readlink: %v", i, err)
		}

		if !strings.Contains(target, "alpha") {
			t.Fatalf(
				"iteration %d: expected alpha to win "+
					"conflict, got target %s", i, target)
		}
	}
}

// --- Behavior 11: Unique temp-link path for concurrent builds ---

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

// --- Behavior 12: Rollback uses unique temp-link path ---

func TestRollbackDoesNotClobberConcurrentTempLink(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

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

	if err := Rollback(galeDir, 1); err != nil {
		t.Fatalf("Rollback error: %v", err)
	}

	if _, err := os.Lstat(otherTmp); err != nil {
		t.Errorf("Rollback clobbered another process's temp link: %v", err)
	}
}
