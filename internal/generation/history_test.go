package generation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListReturnsAllGenerations(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	createStoreEntry(t, storeRoot, "jq", "1.7.1", []string{"jq"})
	createStoreEntry(t, storeRoot, "fd", "9.0", []string{"fd"})
	createStoreEntry(t, storeRoot, "rg", "14.0", []string{"rg"})

	// Gen 1: jq only.
	if err := Build(map[string]string{"jq": "1.7.1"}, galeDir, storeRoot); err != nil {
		t.Fatalf("Build gen 1: %v", err)
	}
	// Gen 2: jq + fd.
	if err := Build(map[string]string{"jq": "1.7.1", "fd": "9.0"}, galeDir, storeRoot); err != nil {
		t.Fatalf("Build gen 2: %v", err)
	}
	// Gen 3: jq + fd + rg.
	if err := Build(map[string]string{"jq": "1.7.1", "fd": "9.0", "rg": "14.0"}, galeDir, storeRoot); err != nil {
		t.Fatalf("Build gen 3: %v", err)
	}

	gens, err := List(galeDir, storeRoot)
	if err != nil {
		t.Fatalf("List error: %v", err)
	}

	if len(gens) != 3 {
		t.Fatalf("expected 3 generations, got %d", len(gens))
	}

	// Verify sorted by number ascending.
	for i, want := range []int{1, 2, 3} {
		if gens[i].Number != want {
			t.Errorf("gens[%d].Number = %d, want %d", i, gens[i].Number, want)
		}
	}

	// Current should be gen 3.
	for i, g := range gens {
		if g.Current != (g.Number == 3) {
			t.Errorf("gens[%d].Current = %v, want %v", i, g.Current, g.Number == 3)
		}
	}

	// Verify package counts.
	if len(gens[0].Packages) != 1 {
		t.Errorf("gen 1 packages = %d, want 1", len(gens[0].Packages))
	}
	if len(gens[1].Packages) != 2 {
		t.Errorf("gen 2 packages = %d, want 2", len(gens[1].Packages))
	}
	if len(gens[2].Packages) != 3 {
		t.Errorf("gen 3 packages = %d, want 3", len(gens[2].Packages))
	}

	// Verify specific packages.
	if v, ok := gens[0].Packages["jq"]; !ok || v != "1.7.1" {
		t.Errorf("gen 1 jq = %q, want 1.7.1", v)
	}
	if v, ok := gens[1].Packages["fd"]; !ok || v != "9.0" {
		t.Errorf("gen 2 fd = %q, want 9.0", v)
	}
}

func TestListSkipsNonNumericDirs(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	createStoreEntry(t, storeRoot, "jq", "1.7.1", []string{"jq"})

	if err := Build(map[string]string{"jq": "1.7.1"}, galeDir, storeRoot); err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Create a non-numeric directory in gen/.
	if err := os.MkdirAll(filepath.Join(galeDir, "gen", "garbage"), 0o755); err != nil {
		t.Fatalf("create garbage dir: %v", err)
	}

	gens, err := List(galeDir, storeRoot)
	if err != nil {
		t.Fatalf("List error: %v", err)
	}

	if len(gens) != 1 {
		t.Fatalf("expected 1 generation, got %d", len(gens))
	}
	if gens[0].Number != 1 {
		t.Errorf("gens[0].Number = %d, want 1", gens[0].Number)
	}
}

func TestDiffShowsAddedAndRemoved(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	createStoreEntry(t, storeRoot, "jq", "1.7.1", []string{"jq"})
	createStoreEntry(t, storeRoot, "fd", "9.0", []string{"fd"})

	// Gen 1: jq only.
	if err := Build(map[string]string{"jq": "1.7.1"}, galeDir, storeRoot); err != nil {
		t.Fatalf("Build gen 1: %v", err)
	}
	// Gen 2: fd only (jq removed).
	if err := Build(map[string]string{"fd": "9.0"}, galeDir, storeRoot); err != nil {
		t.Fatalf("Build gen 2: %v", err)
	}

	d, err := Diff(galeDir, storeRoot, 1, 2)
	if err != nil {
		t.Fatalf("Diff error: %v", err)
	}

	if d.From != 1 || d.To != 2 {
		t.Errorf("Diff From=%d To=%d, want 1→2", d.From, d.To)
	}

	if len(d.Added) != 1 || d.Added[0] != "fd@9.0" {
		t.Errorf("Added = %v, want [fd@9.0]", d.Added)
	}
	if len(d.Removed) != 1 || d.Removed[0] != "jq@1.7.1" {
		t.Errorf("Removed = %v, want [jq@1.7.1]", d.Removed)
	}
}

func TestDiffShowsVersionChanges(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	createStoreEntry(t, storeRoot, "jq", "1.7.1", []string{"jq"})
	createStoreEntry(t, storeRoot, "jq", "1.8.0", []string{"jq"})

	// Gen 1: jq 1.7.1.
	if err := Build(map[string]string{"jq": "1.7.1"}, galeDir, storeRoot); err != nil {
		t.Fatalf("Build gen 1: %v", err)
	}
	// Gen 2: jq 1.8.0 (upgraded).
	if err := Build(map[string]string{"jq": "1.8.0"}, galeDir, storeRoot); err != nil {
		t.Fatalf("Build gen 2: %v", err)
	}

	d, err := Diff(galeDir, storeRoot, 1, 2)
	if err != nil {
		t.Fatalf("Diff error: %v", err)
	}

	// Version change shows as both added and removed.
	if len(d.Added) != 1 || d.Added[0] != "jq@1.8.0" {
		t.Errorf("Added = %v, want [jq@1.8.0]", d.Added)
	}
	if len(d.Removed) != 1 || d.Removed[0] != "jq@1.7.1" {
		t.Errorf("Removed = %v, want [jq@1.7.1]", d.Removed)
	}
}

func TestRollbackSwapsCurrent(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := filepath.Join(galeDir, "pkg")

	createStoreEntry(t, storeRoot, "jq", "1.7.1", []string{"jq"})
	createStoreEntry(t, storeRoot, "fd", "9.0", []string{"fd"})

	// Build gen 1 and gen 2.
	if err := Build(map[string]string{"jq": "1.7.1"}, galeDir, storeRoot); err != nil {
		t.Fatalf("Build gen 1: %v", err)
	}
	if err := Build(map[string]string{"jq": "1.7.1", "fd": "9.0"}, galeDir, storeRoot); err != nil {
		t.Fatalf("Build gen 2: %v", err)
	}

	// Current should be 2.
	cur, err := Current(galeDir)
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if cur != 2 {
		t.Fatalf("expected current=2, got %d", cur)
	}

	// Rollback to gen 1.
	if err := Rollback(galeDir, storeRoot, 1); err != nil {
		t.Fatalf("Rollback error: %v", err)
	}

	// Current should now be 1.
	cur, err = Current(galeDir)
	if err != nil {
		t.Fatalf("Current after rollback: %v", err)
	}
	if cur != 1 {
		t.Errorf("expected current=1 after rollback, got %d", cur)
	}

	// Gen 1 symlinks should still work.
	jqLink := filepath.Join(galeDir, "current", "bin", "jq")
	if _, err := os.Stat(jqLink); err != nil {
		t.Errorf("jq symlink should be accessible after rollback: %v", err)
	}
}

func TestRollbackNonexistentGeneration(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := filepath.Join(galeDir, "pkg")

	createStoreEntry(t, storeRoot, "jq", "1.7.1", []string{"jq"})

	if err := Build(map[string]string{"jq": "1.7.1"}, galeDir, storeRoot); err != nil {
		t.Fatalf("Build: %v", err)
	}

	err := Rollback(galeDir, storeRoot, 99)
	if err == nil {
		t.Fatal("expected Rollback to non-existent generation to return error")
	}
}

// TestGenVersionsDanglingSymlinkSkipped pins the dangling-symlink
// behavior of the unified gen-dir reader: a generation that
// contains a dangling symlink (simulating a GC'd package) must
// not cause List, Diff, or CurrentVersions to error or return a
// phantom entry for the GC'd package. Only the live package must
// appear.
func TestGenVersionsDanglingSymlinkSkipped(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := filepath.Join(galeDir, "pkg")

	// Build a real generation with one live package.
	createStoreEntry(t, storeRoot, "jq", "1.7.1", []string{"jq"})
	if err := Build(map[string]string{"jq": "1.7.1"}, galeDir, storeRoot); err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Inject a dangling symlink into gen/1/bin/, simulating a GC'd package.
	genBinDir := filepath.Join(galeDir, "gen", "1", "bin")
	danglingLink := filepath.Join(genBinDir, "ghost")
	// Point at a store dir that doesn't exist.
	if err := os.Symlink(
		filepath.Join(storeRoot, "ghost", "9.9.9", "bin", "ghost"),
		danglingLink,
	); err != nil {
		t.Fatalf("create dangling symlink: %v", err)
	}

	// List must return gen 1 with exactly one package (jq).
	gens, err := List(galeDir, storeRoot)
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(gens) != 1 {
		t.Fatalf("expected 1 generation, got %d", len(gens))
	}
	if len(gens[0].Packages) != 1 {
		t.Errorf("gen 1 packages = %v, want {jq:1.7.1} only (dangling ghost must be skipped)",
			gens[0].Packages)
	}
	if v, ok := gens[0].Packages["jq"]; !ok || v != "1.7.1" {
		t.Errorf("gen 1 jq = %q (ok=%v), want 1.7.1", v, ok)
	}

	// CurrentVersions must also see only jq.
	cur, err := CurrentVersions(galeDir, storeRoot)
	if err != nil {
		t.Fatalf("CurrentVersions error: %v", err)
	}
	if len(cur) != 1 {
		t.Errorf("CurrentVersions = %v, want {jq:1.7.1} only", cur)
	}
	if v, ok := cur["jq"]; !ok || v != "1.7.1" {
		t.Errorf("CurrentVersions[jq] = %q (ok=%v), want 1.7.1", v, ok)
	}

	// Build a second generation so Diff and Rollback have two gens to work with.
	// Gen 2 has the same packages as gen 1 (jq only).
	if err := Build(map[string]string{"jq": "1.7.1"}, galeDir, storeRoot); err != nil {
		t.Fatalf("Build gen 2: %v", err)
	}

	// Diff gen 1 → gen 2 must show no changes. The dangling ghost in gen 1
	// must not appear as a removed package.
	d, err := Diff(galeDir, storeRoot, 1, 2)
	if err != nil {
		t.Fatalf("Diff error: %v", err)
	}
	if len(d.Added) != 0 || len(d.Removed) != 0 {
		t.Errorf("Diff(1,2) = added=%v removed=%v, want empty (dangling ghost must not appear)",
			d.Added, d.Removed)
	}

	// Rollback to gen 1 must REFUSE. This assertion is inverted
	// from what it was, and deliberately so: the reader contract
	// this test is about — genVersions skipping a dangling link,
	// checked above through List, CurrentVersions and Diff — is
	// unchanged. What changed is the gate above it. Activating a
	// generation with a dangling symlink puts a broken entry on
	// PATH, and Build has always refused to do it; Rollback
	// refusing too is gh#247's second half, and the reason the old
	// expectation could stand was that nobody had asked what a
	// successful rollback onto a swept closure leaves behind.
	err = Rollback(galeDir, storeRoot, 1)
	if err == nil {
		t.Fatal("Rollback to gen 1 with a dangling symlink must " +
			"refuse: reading past a dangling link is the history " +
			"contract, activating one is not")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("refusal must name the dangling link, got: %v", err)
	}
	// Current must be unchanged — gen 2, where the second Build
	// left it.
	n, err := Current(galeDir)
	if err != nil {
		t.Fatalf("Current after refused rollback: %v", err)
	}
	if n != 2 {
		t.Errorf("current = %d after a refused rollback, want 2", n)
	}
}

// TestGenVersionsLibOnlyPackageVisible tests that a package
// installed only into lib/ (no bin/ entries) is visible in List
// and CurrentVersions. The unified full-tree walk must surface
// lib-only packages that the old bin-only packagesFromGen missed.
func TestGenVersionsLibOnlyPackageVisible(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := filepath.Join(galeDir, "pkg")

	// Create a package with only a lib/ entry (no bin/).
	libDir := filepath.Join(storeRoot, "libfoo", "1.0", "lib")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatalf("create lib dir: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(libDir, "libfoo.so"), []byte("fake"), 0o644,
	); err != nil {
		t.Fatalf("create lib file: %v", err)
	}

	// Also a regular package with a bin/.
	createStoreEntry(t, storeRoot, "jq", "1.7.1", []string{"jq"})

	pkgs := map[string]string{"jq": "1.7.1", "libfoo": "1.0"}
	if err := Build(pkgs, galeDir, storeRoot); err != nil {
		t.Fatalf("Build: %v", err)
	}

	gens, err := List(galeDir, storeRoot)
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(gens) != 1 {
		t.Fatalf("expected 1 generation, got %d", len(gens))
	}

	// Both packages must appear.
	if len(gens[0].Packages) != 2 {
		t.Errorf("gen 1 packages = %v, want jq and libfoo", gens[0].Packages)
	}
	if v, ok := gens[0].Packages["libfoo"]; !ok || v != "1.0" {
		t.Errorf("gen 1 libfoo = %q (ok=%v), want 1.0", v, ok)
	}

	// CurrentVersions must also include libfoo.
	cur, err := CurrentVersions(galeDir, storeRoot)
	if err != nil {
		t.Fatalf("CurrentVersions error: %v", err)
	}
	if v, ok := cur["libfoo"]; !ok || v != "1.0" {
		t.Errorf("CurrentVersions[libfoo] = %q (ok=%v), want 1.0 (lib-only package must appear)",
			v, ok)
	}
}

// TestRollbackRefusesGenerationWithDanglingSymlink pins gh#247's
// second half. Build refuses to activate a generation with a
// dangling symlink — validateGenerationSymlinks runs between
// populate and swap — and Rollback is the same activation, so it
// needs the same gate.
//
// Without it Rollback stat'd the directory, read the shrunken
// package set through the LENIENT reader (which skips a package
// whose store dir is gone, by contract), staged the farm from
// that, and swapped. The user got a successful rollback onto a
// broken PATH.
//
// Repair is not the alternative: rebuilding the generation here
// would invert the generation → installer package order, and
// carrying a different version forward would break the
// one-number-one-snapshot invariant (gh#189).
func TestRollbackRefusesGenerationWithDanglingSymlink(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := filepath.Join(galeDir, "pkg")

	createStoreEntry(t, storeRoot, "jq", "1.7.1", []string{"jq"})
	createStoreEntry(t, storeRoot, "fd", "9.0", []string{"fd"})
	if err := Build(
		map[string]string{"jq": "1.7.1"}, galeDir, storeRoot,
	); err != nil {
		t.Fatalf("Build gen 1: %v", err)
	}
	if err := Build(
		map[string]string{"jq": "1.7.1", "fd": "9.0"},
		galeDir, storeRoot,
	); err != nil {
		t.Fatalf("Build gen 2: %v", err)
	}
	if err := Rollback(galeDir, storeRoot, 1); err != nil {
		t.Fatalf("Rollback to gen 1: %v", err)
	}
	// gen/2's fd leaves the store, exactly as a gc run under the
	// old retention rule left it.
	if err := os.RemoveAll(
		filepath.Join(storeRoot, "fd", "9.0"),
	); err != nil {
		t.Fatal(err)
	}

	err := Rollback(galeDir, storeRoot, 2)
	if err == nil {
		t.Fatal("Rollback onto a generation with a dangling " +
			"symlink must refuse — activating it puts a broken " +
			"entry on PATH")
	}
	for _, want := range []string{"generation 2", "gale sync"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal must mention %q, got: %v", want, err)
		}
	}

	cur, cErr := Current(galeDir)
	if cErr != nil {
		t.Fatalf("Current after refused rollback: %v", cErr)
	}
	if cur != 1 {
		t.Errorf("current = gen/%d after a refused rollback, want "+
			"gen/1 — a refusal must leave the active generation "+
			"and the farm untouched", cur)
	}
}
