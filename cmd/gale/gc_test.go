package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/recipe"
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

	// Empty store — no entries to resolve against. mergeConfig
	// should fall back to the raw config keys so unresolved
	// references still register.
	s := store.NewStore(t.TempDir())
	out := output.New(os.Stderr, false)
	ref := collectReferencedPackages(globalDir, projCfg, s, out)

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

	s := store.NewStore(t.TempDir())
	out := output.New(os.Stderr, false)
	ref := collectReferencedPackages(globalDir, "", s, out)

	if len(ref) != 1 {
		t.Fatalf("got %d entries, want 1: %v",
			len(ref), ref)
	}
	if !ref["jq@1.7"] {
		t.Error("missing jq@1.7")
	}
}

// TestCollectReferencedPackagesResolvesBareToCanonical verifies
// that when the store holds a canonical revision dir (jq/1.8.1-3)
// but config uses a bare version (jq = "1.8.1"), the referenced
// set is keyed on the resolved on-disk name. This is what keeps
// gc and doctor's orphan check from treating the live entry as
// unreferenced.
func TestCollectReferencedPackagesResolvesBareToCanonical(t *testing.T) {
	storeRoot := t.TempDir()
	if err := os.MkdirAll(
		filepath.Join(storeRoot, "jq", "1.8.1-3", "bin"),
		0o755); err != nil {
		t.Fatal(err)
	}

	globalDir := t.TempDir()
	globalCfg := filepath.Join(globalDir, "gale.toml")
	if err := os.WriteFile(globalCfg,
		[]byte("[packages]\njq = \"1.8.1\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	s := store.NewStore(storeRoot)
	out := output.New(os.Stderr, false)
	ref := collectReferencedPackages(globalDir, "", s, out)

	if !ref["jq@1.8.1-3"] {
		t.Errorf("expected referenced[jq@1.8.1-3] = true "+
			"(canonical resolution of bare jq@1.8.1), got: %v",
			ref)
	}
	if ref["jq@1.8.1"] {
		t.Error("bare key jq@1.8.1 must not appear — " +
			"referenced set should only carry resolved keys")
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

// TestGCKeepsCanonicalForBareRef pins the v0.12.0 regression
// where `gale gc` deleted store entries actively referenced by
// the generation. gale.toml lists packages by bare version
// (jq = "1.8.1"), but the store writes canonical revision-
// suffixed dirs (jq/1.8.1-2/). gc must treat these as
// referenced or it takes out live store entries.
//
// collectReferencedPackages resolves each config entry through
// the store, so bare/canonical comparisons always line up.
func TestGCKeepsCanonicalForBareRef(t *testing.T) {
	storeRoot := t.TempDir()
	for _, ver := range []string{"1.8.1-2", "1.7.1-1"} {
		dir := filepath.Join(storeRoot, "jq", ver, "bin")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	globalDir := t.TempDir()
	globalCfg := filepath.Join(globalDir, "gale.toml")
	if err := os.WriteFile(globalCfg,
		[]byte("[packages]\njq = \"1.8.1\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	s := store.NewStore(storeRoot)
	out := output.New(os.Stderr, false)

	ref := collectReferencedPackages(globalDir, "", s, out)
	n := removeUnreferencedVersions(s, ref, false, out)

	if n != 1 {
		t.Errorf("want 1 removed, got %d", n)
	}
	if _, err := os.Stat(filepath.Join(
		storeRoot, "jq", "1.8.1-2")); err != nil {
		t.Errorf("jq/1.8.1-2 must survive — canonical match "+
			"for bare jq@1.8.1: %v", err)
	}
	if _, err := os.Stat(filepath.Join(
		storeRoot, "jq", "1.7.1-1")); !os.IsNotExist(err) {
		t.Errorf("jq/1.7.1-1 should have been removed")
	}
}

// TestGCReapsOldRevisionsWhenConfigIsBare verifies that when
// multiple revisions of the same version are on disk and config
// references the bare version, gc removes older revisions and
// keeps only the highest (which is what StorePath resolves a
// bare version to). Regression fix for the farm-drift loop
// where inactive revisions lingered forever.
func TestGCReapsOldRevisionsWhenConfigIsBare(t *testing.T) {
	storeRoot := t.TempDir()
	for _, ver := range []string{"1.8.1-2", "1.8.1-3"} {
		dir := filepath.Join(storeRoot, "jq", ver, "bin")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	globalDir := t.TempDir()
	globalCfg := filepath.Join(globalDir, "gale.toml")
	if err := os.WriteFile(globalCfg,
		[]byte("[packages]\njq = \"1.8.1\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	s := store.NewStore(storeRoot)
	out := output.New(os.Stderr, false)

	ref := collectReferencedPackages(globalDir, "", s, out)
	n := removeUnreferencedVersions(s, ref, false, out)
	if n != 1 {
		t.Errorf("want 1 removed, got %d", n)
	}
	if _, err := os.Stat(filepath.Join(
		storeRoot, "jq", "1.8.1-3")); err != nil {
		t.Errorf("jq/1.8.1-3 should survive (highest rev = "+
			"canonical for bare jq@1.8.1): %v", err)
	}
	if _, err := os.Stat(filepath.Join(
		storeRoot, "jq", "1.8.1-2")); !os.IsNotExist(err) {
		t.Errorf("jq/1.8.1-2 should be removed")
	}
}

// TestGCKeepsExplicitlyPinnedRevision verifies that when config
// pins a specific revision (jq = "1.8.1-2"), gc keeps exactly
// that revision and reaps others.
func TestGCKeepsExplicitlyPinnedRevision(t *testing.T) {
	storeRoot := t.TempDir()
	for _, ver := range []string{"1.8.1-2", "1.8.1-3"} {
		dir := filepath.Join(storeRoot, "jq", ver, "bin")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	globalDir := t.TempDir()
	globalCfg := filepath.Join(globalDir, "gale.toml")
	if err := os.WriteFile(globalCfg,
		[]byte("[packages]\njq = \"1.8.1-2\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	s := store.NewStore(storeRoot)
	out := output.New(os.Stderr, false)

	ref := collectReferencedPackages(globalDir, "", s, out)
	n := removeUnreferencedVersions(s, ref, false, out)
	if n != 1 {
		t.Errorf("want 1 removed, got %d", n)
	}
	if _, err := os.Stat(filepath.Join(
		storeRoot, "jq", "1.8.1-2")); err != nil {
		t.Errorf("jq/1.8.1-2 should survive (explicit pin): %v", err)
	}
	if _, err := os.Stat(filepath.Join(
		storeRoot, "jq", "1.8.1-3")); !os.IsNotExist(err) {
		t.Errorf("jq/1.8.1-3 should be removed")
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

// makeTestRecipe builds a minimal recipe usable as a fake
// resolver result. Runtime/build dep names flow through
// Dependencies.{Runtime,Build}.
func makeTestRecipe(name, version string, revision int,
	runtime, build []string,
) *recipe.Recipe {
	return &recipe.Recipe{
		Package: recipe.Package{
			Name:     name,
			Version:  version,
			Revision: revision,
		},
		Dependencies: recipe.Dependencies{
			Build:   build,
			Runtime: runtime,
		},
	}
}

func recipeResolverFromMap(
	m map[string]*recipe.Recipe,
) installer.RecipeResolver {
	return func(name string) (*recipe.Recipe, error) {
		r, ok := m[name]
		if !ok {
			return nil, fmt.Errorf("no recipe for %s", name)
		}
		return r, nil
	}
}

// TestCollectReferencedPackagesIncludesRuntimeDeps verifies
// that when a config package has runtime dependencies, those
// deps' installed revisions are kept by gc even though they
// aren't listed in gale.toml. Prevents gc from reaping
// `readline@8.2-2` out from under a running `postgresql`
// that links against it.
func TestCollectReferencedPackagesIncludesRuntimeDeps(t *testing.T) {
	storeRoot := t.TempDir()
	for _, d := range []struct{ n, v string }{
		{"postgresql", "17.2-1"},
		{"readline", "8.2-2"},
		{"bison", "3.8.2-2"},
	} {
		if err := os.MkdirAll(
			filepath.Join(storeRoot, d.n, d.v, "bin"),
			0o755); err != nil {
			t.Fatal(err)
		}
	}

	globalDir := t.TempDir()
	globalCfg := filepath.Join(globalDir, "gale.toml")
	if err := os.WriteFile(globalCfg,
		[]byte("[packages]\npostgresql = \"17.2\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	s := store.NewStore(storeRoot)
	out := output.New(os.Stderr, false)

	resolver := recipeResolverFromMap(map[string]*recipe.Recipe{
		"postgresql": makeTestRecipe("postgresql", "17.2", 1,
			[]string{"readline"}, []string{"bison"}),
		"readline": makeTestRecipe("readline", "8.2", 2, nil, nil),
		"bison":    makeTestRecipe("bison", "3.8.2", 2, nil, nil),
	})

	ref := collectReferencedPackagesWithResolver(
		globalDir, "", s, resolver, out)

	if !ref["postgresql@17.2-1"] {
		t.Errorf("missing postgresql@17.2-1: %v", ref)
	}
	if !ref["readline@8.2-2"] {
		t.Errorf("runtime dep readline@8.2-2 must be " +
			"referenced — gc would otherwise delete it " +
			"out from under postgres")
	}
	if ref["bison@3.8.2-2"] {
		t.Errorf("build-only dep bison@3.8.2-2 must NOT " +
			"be referenced — user opted to reap build deps")
	}
}

// TestCollectReferencedPackagesRuntimeDepsTransitive verifies
// that runtime deps are expanded transitively — a config
// package's runtime dep's runtime deps are also retained.
func TestCollectReferencedPackagesRuntimeDepsTransitive(t *testing.T) {
	storeRoot := t.TempDir()
	for _, d := range []struct{ n, v string }{
		{"curl", "8.19.0-1"},
		{"openssl", "3.6.1-2"},
		{"zlib", "1.3.2-2"},
	} {
		if err := os.MkdirAll(
			filepath.Join(storeRoot, d.n, d.v, "lib"),
			0o755); err != nil {
			t.Fatal(err)
		}
	}

	globalDir := t.TempDir()
	globalCfg := filepath.Join(globalDir, "gale.toml")
	if err := os.WriteFile(globalCfg,
		[]byte("[packages]\ncurl = \"8.19.0\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	s := store.NewStore(storeRoot)
	out := output.New(os.Stderr, false)

	resolver := recipeResolverFromMap(map[string]*recipe.Recipe{
		"curl": makeTestRecipe("curl", "8.19.0", 1,
			[]string{"openssl"}, nil),
		"openssl": makeTestRecipe("openssl", "3.6.1", 2,
			[]string{"zlib"}, nil),
		"zlib": makeTestRecipe("zlib", "1.3.2", 2, nil, nil),
	})

	ref := collectReferencedPackagesWithResolver(
		globalDir, "", s, resolver, out)

	for _, k := range []string{
		"curl@8.19.0-1", "openssl@3.6.1-2", "zlib@1.3.2-2",
	} {
		if !ref[k] {
			t.Errorf("transitive runtime dep %q missing: %v",
				k, ref)
		}
	}
}

// TestCollectReferencedPackagesNilResolverFallsBackToConfig
// verifies that when no resolver is available (user has no
// recipes repo synced), gc behaves like it did before runtime
// expansion: only packages in config are kept.
func TestCollectReferencedPackagesNilResolverFallsBackToConfig(t *testing.T) {
	storeRoot := t.TempDir()
	if err := os.MkdirAll(
		filepath.Join(storeRoot, "curl", "8.19.0-1", "bin"),
		0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(
		filepath.Join(storeRoot, "openssl", "3.6.1-2", "lib"),
		0o755); err != nil {
		t.Fatal(err)
	}

	globalDir := t.TempDir()
	globalCfg := filepath.Join(globalDir, "gale.toml")
	if err := os.WriteFile(globalCfg,
		[]byte("[packages]\ncurl = \"8.19.0\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	s := store.NewStore(storeRoot)
	out := output.New(os.Stderr, false)

	ref := collectReferencedPackagesWithResolver(
		globalDir, "", s, nil, out)

	if !ref["curl@8.19.0-1"] {
		t.Errorf("curl missing: %v", ref)
	}
	if ref["openssl@3.6.1-2"] {
		t.Errorf("openssl should not be referenced without " +
			"a resolver — falls back to config-only")
	}
}
