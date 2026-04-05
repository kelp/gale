package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/store"
)

func TestCollectReferencedPackages(t *testing.T) {
	// Set up a global config dir with two packages.
	globalDir := t.TempDir()
	globalCfg := filepath.Join(globalDir, "gale.toml")
	if err := os.WriteFile(globalCfg, []byte(
		"[packages]\njq = \"1.7\"\nfd = \"9.0\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	// Set up a project config with one overlapping and
	// one unique package.
	projDir := t.TempDir()
	projCfg := filepath.Join(projDir, "gale.toml")
	if err := os.WriteFile(projCfg, []byte(
		"[packages]\njq = \"1.6\"\nripgrep = \"14.1\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	ref := collectReferencedPackages(globalDir, projCfg)

	want := map[string]bool{
		"jq@1.7":       true,
		"fd@9.0":       true,
		"jq@1.6":       true,
		"ripgrep@14.1": true,
	}
	if len(ref) != len(want) {
		t.Fatalf("got %d entries, want %d: %v",
			len(ref), len(want), ref)
	}
	for k := range want {
		if !ref[k] {
			t.Errorf("missing %s", k)
		}
	}
}

func TestCollectReferencedPackagesNoProject(t *testing.T) {
	// Only global config, no project config.
	globalDir := t.TempDir()
	globalCfg := filepath.Join(globalDir, "gale.toml")
	if err := os.WriteFile(globalCfg, []byte(
		"[packages]\njq = \"1.7\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	ref := collectReferencedPackages(globalDir, "")

	if len(ref) != 1 {
		t.Fatalf("got %d entries, want 1: %v",
			len(ref), ref)
	}
	if !ref["jq@1.7"] {
		t.Error("missing jq@1.7")
	}
}

func TestRemoveUnreferencedVersions(t *testing.T) {
	// Set up a store with three packages.
	storeRoot := t.TempDir()
	for _, pkg := range []struct{ name, ver string }{
		{"jq", "1.7"},
		{"fd", "9.0"},
		{"ripgrep", "14.1"},
	} {
		dir := filepath.Join(
			storeRoot, pkg.name, pkg.ver, "bin")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	s := store.NewStore(storeRoot)
	out := output.New(os.Stderr, false)

	// Only jq@1.7 is referenced.
	referenced := map[string]bool{"jq@1.7": true}

	// Dry run — nothing removed.
	n := removeUnreferencedVersions(
		s, referenced, true, out)
	if n != 2 {
		t.Errorf("dry-run: want 2 flagged, got %d", n)
	}
	// All dirs still exist.
	installed, _ := s.List()
	if len(installed) != 3 {
		t.Errorf("dry-run: want 3 installed, got %d",
			len(installed))
	}

	// Real run.
	n = removeUnreferencedVersions(
		s, referenced, false, out)
	if n != 2 {
		t.Errorf("want 2 removed, got %d", n)
	}
	// Only jq@1.7 survives.
	installed, _ = s.List()
	if len(installed) != 1 {
		t.Fatalf("want 1 installed, got %d", len(installed))
	}
	if installed[0].Name != "jq" ||
		installed[0].Version != "1.7" {
		t.Errorf("want jq@1.7, got %s@%s",
			installed[0].Name, installed[0].Version)
	}
}

func TestRemoveUnreferencedVersionsNoneToRemove(t *testing.T) {
	storeRoot := t.TempDir()
	dir := filepath.Join(storeRoot, "jq", "1.7", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	s := store.NewStore(storeRoot)
	out := output.New(os.Stderr, false)
	referenced := map[string]bool{"jq@1.7": true}

	n := removeUnreferencedVersions(
		s, referenced, false, out)
	if n != 0 {
		t.Errorf("want 0 removed, got %d", n)
	}
}

func TestGCCommandExists(t *testing.T) {
	for _, c := range rootCmd.Commands() {
		if c.Name() == "gc" {
			return
		}
	}
	t.Fatal("gc command not found on rootCmd")
}

// TestCleanGenerationsRemovesOldDirs verifies that gc
// removes generation directories other than the current
// one. We set up a fake gale dir with gen/1, gen/2,
// gen/3 and current -> gen/3/bin, then verify only
// gen/3 survives.
func TestCleanGenerationsRemovesOldDirs(t *testing.T) {
	galeDir := t.TempDir()
	genRoot := filepath.Join(galeDir, "gen")

	// Create three generation directories.
	for _, n := range []string{"1", "2", "3"} {
		dir := filepath.Join(genRoot, n, "bin")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Point current -> gen/3 (relative symlink like
	// generation.Build creates).
	currentPath := filepath.Join(galeDir, "current")
	if err := os.Symlink(
		filepath.Join("gen", "3"), currentPath); err != nil {
		t.Fatal(err)
	}

	// Run gc in dry-run mode first — nothing removed.
	dryRun = true
	t.Cleanup(func() { dryRun = false })

	// Call cleanOldGenerations directly.
	removed := cleanOldGenerations(galeDir, true)
	if removed != 2 {
		t.Errorf("dry-run: want 2 flagged, got %d", removed)
	}
	// All dirs still exist.
	for _, n := range []string{"1", "2", "3"} {
		if _, err := os.Stat(
			filepath.Join(genRoot, n)); err != nil {
			t.Errorf("dry-run: gen/%s should still exist", n)
		}
	}

	// Now run for real.
	dryRun = false
	removed = cleanOldGenerations(galeDir, false)
	if removed != 2 {
		t.Errorf("want 2 removed, got %d", removed)
	}

	// gen/3 must survive, gen/1 and gen/2 must be gone.
	if _, err := os.Stat(
		filepath.Join(genRoot, "3")); err != nil {
		t.Error("gen/3 should still exist")
	}
	for _, n := range []string{"1", "2"} {
		if _, err := os.Stat(
			filepath.Join(genRoot, n)); !os.IsNotExist(err) {
			t.Errorf("gen/%s should have been removed", n)
		}
	}
}

// TestGCSummaryDistinguishesVersionsAndGenerations
// verifies that the gc summary reports separate counts
// for package versions and generation directories
// rather than conflating them into a single counter.
func TestGCSummaryDistinguishesVersionsAndGenerations(t *testing.T) {
	// Create a project dir with an empty config (no
	// referenced packages) and a store with one
	// unreferenced package plus old generations.
	projDir := t.TempDir()
	configPath := filepath.Join(projDir, "gale.toml")
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Set up the store with an unreferenced package.
	storeRoot := filepath.Join(projDir, "store")
	pkgDir := filepath.Join(
		storeRoot, "oldpkg", "0.1", "bin")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Set up generations: gen/1 (old), gen/2 (current).
	galeDir := filepath.Join(projDir, ".gale")
	for _, n := range []string{"1", "2"} {
		d := filepath.Join(galeDir, "gen", n, "bin")
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(
		filepath.Join("gen", "2"),
		filepath.Join(galeDir, "current")); err != nil {
		t.Fatal(err)
	}

	// Run gc in dry-run mode and capture stderr.
	orig, _ := os.Getwd()
	os.Chdir(projDir)
	t.Cleanup(func() { os.Chdir(orig) })

	dryRun = true
	t.Cleanup(func() { dryRun = false })

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStderr := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = origStderr })

	runErr := gcCmd.RunE(gcCmd, nil)
	w.Close()

	var buf [8192]byte
	n, _ := r.Read(buf[:])
	stderr := string(buf[:n])
	os.Stderr = origStderr

	if runErr != nil {
		t.Fatalf("gc command failed: %v", runErr)
	}

	// The summary should mention "version(s)" and
	// "generation(s)" separately rather than combining
	// them into a single count.
	if !strings.Contains(stderr, "version(s)") {
		t.Errorf("expected 'version(s)' in summary, "+
			"got %q", stderr)
	}
	if !strings.Contains(stderr, "generation(s)") {
		t.Errorf("expected 'generation(s)' in summary, "+
			"got %q", stderr)
	}
}
