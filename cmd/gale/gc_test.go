package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/depsmeta"
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
		"[packages]\njq = \"1.7\"\nfd = \"9.0\"\n",
	),
		0o644); err != nil {
		t.Fatal(err)
	}

	// Set up a project config with one overlapping and
	// one unique package.
	projDir := t.TempDir()
	projCfg := filepath.Join(projDir, "gale.toml")
	if err := os.WriteFile(projCfg, []byte(
		"[packages]\njq = \"1.6\"\nripgrep = \"14.1\"\n",
	),
		0o644); err != nil {
		t.Fatal(err)
	}

	// Empty store — no entries to resolve against. mergeConfig
	// should fall back to the raw config keys so unresolved
	// references still register.
	s := store.NewStore(t.TempDir())
	ref, err := collectReferencedPackagesWithResolver(globalDir, projCfg, s, nil, nil)
	if err != nil {
		t.Fatalf("collecting references: %v", err)
	}

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
		"[packages]\njq = \"1.7\"\n",
	),
		0o644); err != nil {
		t.Fatal(err)
	}

	s := store.NewStore(t.TempDir())
	ref, err := collectReferencedPackagesWithResolver(globalDir, "", s, nil, nil)
	if err != nil {
		t.Fatalf("collecting references: %v", err)
	}

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
		0o755,
	); err != nil {
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
	ref, err := collectReferencedPackagesWithResolver(globalDir, "", s, nil, nil)
	if err != nil {
		t.Fatalf("collecting references: %v", err)
	}

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
			storeRoot, pkg.name, pkg.ver, "bin",
		)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	s := store.NewStore(storeRoot)
	out := output.New(os.Stderr, false)

	// Only jq@1.7 is referenced.
	referenced := map[string]bool{"jq@1.7": true}

	// Dry run — nothing removed.
	n, _ := removeUnreferencedVersions(
		s, referenced, true, out,
	)
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
	n, _ = removeUnreferencedVersions(
		s, referenced, false, out,
	)
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

	n, _ := removeUnreferencedVersions(
		s, referenced, false, out,
	)
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
// collectReferencedPackagesWithResolver resolves each config
// entry through the store, so bare/canonical comparisons
// always line up.
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

	ref, err := collectReferencedPackagesWithResolver(globalDir, "", s, nil, nil)
	if err != nil {
		t.Fatalf("collecting references: %v", err)
	}
	n, _ := removeUnreferencedVersions(s, ref, false, out)

	if n != 1 {
		t.Errorf("want 1 removed, got %d", n)
	}
	if _, err := os.Stat(filepath.Join(
		storeRoot, "jq", "1.8.1-2",
	)); err != nil {
		t.Errorf("jq/1.8.1-2 must survive — canonical match "+
			"for bare jq@1.8.1: %v", err)
	}
	if _, err := os.Stat(filepath.Join(
		storeRoot, "jq", "1.7.1-1",
	)); !os.IsNotExist(err) {
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

	ref, err := collectReferencedPackagesWithResolver(globalDir, "", s, nil, nil)
	if err != nil {
		t.Fatalf("collecting references: %v", err)
	}
	n, _ := removeUnreferencedVersions(s, ref, false, out)
	if n != 1 {
		t.Errorf("want 1 removed, got %d", n)
	}
	if _, err := os.Stat(filepath.Join(
		storeRoot, "jq", "1.8.1-3",
	)); err != nil {
		t.Errorf("jq/1.8.1-3 should survive (highest rev = "+
			"canonical for bare jq@1.8.1): %v", err)
	}
	if _, err := os.Stat(filepath.Join(
		storeRoot, "jq", "1.8.1-2",
	)); !os.IsNotExist(err) {
		t.Errorf("jq/1.8.1-2 should be removed")
	}
}

// TestGCRemovesOrphanRevisionAboveRecipe verifies gc drops a
// store revision higher than the recipe's current revision
// when a resolver is available (gh#137).
func TestGCRemovesOrphanRevisionAboveRecipe(t *testing.T) {
	storeRoot := t.TempDir()
	for _, ver := range []string{"1.48.0-1", "1.48.0-2"} {
		dir := filepath.Join(storeRoot, "just", ver, "bin")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	globalDir := t.TempDir()
	globalCfg := filepath.Join(globalDir, "gale.toml")
	if err := os.WriteFile(globalCfg,
		[]byte("[packages]\njust = \"1.48.0\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	s := store.NewStore(storeRoot)
	out := output.New(os.Stderr, false)

	pinResolve := versionedRecipeResolver(func(name, version string) (*recipe.Recipe, error) {
		if name != "just" || version != "1.48.0" {
			return nil, fmt.Errorf("unexpected pin %s@%s", name, version)
		}
		return &recipe.Recipe{
			Package: recipe.Package{
				Name: "just", Version: "1.48.0", Revision: 1,
			},
		}, nil
	})

	ref, err := collectReferencedPackagesAllHosts(globalDir, "", s, pinResolve)
	if err != nil {
		t.Fatalf("collecting references: %v", err)
	}
	n, _ := removeUnreferencedVersions(s, ref, false, out)
	if n != 1 {
		t.Errorf("want 1 removed, got %d", n)
	}
	if !ref["just@1.48.0-1"] {
		t.Errorf("just@1.48.0-1 must be retained, got: %v", ref)
	}
	if ref["just@1.48.0-2"] {
		t.Error("just@1.48.0-2 must not be retained")
	}
	if _, err := os.Stat(filepath.Join(
		storeRoot, "just", "1.48.0-1",
	)); err != nil {
		t.Errorf("just/1.48.0-1 should survive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(
		storeRoot, "just", "1.48.0-2",
	)); !os.IsNotExist(err) {
		t.Errorf("just/1.48.0-2 should be removed")
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

	ref, err := collectReferencedPackagesWithResolver(globalDir, "", s, nil, nil)
	if err != nil {
		t.Fatalf("collecting references: %v", err)
	}
	n, _ := removeUnreferencedVersions(s, ref, false, out)
	if n != 1 {
		t.Errorf("want 1 removed, got %d", n)
	}
	if _, err := os.Stat(filepath.Join(
		storeRoot, "jq", "1.8.1-2",
	)); err != nil {
		t.Errorf("jq/1.8.1-2 should survive (explicit pin): %v", err)
	}
	if _, err := os.Stat(filepath.Join(
		storeRoot, "jq", "1.8.1-3",
	)); !os.IsNotExist(err) {
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

// TestGCShortMentionsGenerations verifies that gcCmd.Short
// mentions generation cleanup, not just package version removal.
// Users need to know that gc also cleans old generations.
func TestGCShortMentionsGenerations(t *testing.T) {
	if !strings.Contains(gcCmd.Short, "generation") {
		t.Errorf("gcCmd.Short %q does not mention "+
			"\"generation\" — short description must "+
			"cover both package version and generation cleanup",
			gcCmd.Short)
	}
}

// TestCleanGenerationsRemovesOldDirs verifies that gc
// removes generation directories other than the current
// one. We set up a fake gale dir with gen/1, gen/2,
// gen/3 and current -> gen/3/bin, then verify only
// gen/3 survives.
//
// keep = 1 is passed explicitly to preserve that meaning. gc
// retains `[generation] keep` generations now, not just the
// current one (gh#247), so under the default this fixture would
// legitimately remove nothing; keep = 1 is the setting that means
// "the current generation only", which is what this test is
// about. The assertions are unchanged.
func TestCleanGenerationsRemovesOldDirs(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := filepath.Join(galeDir, "pkg")
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
		filepath.Join("gen", "3"), currentPath,
	); err != nil {
		t.Fatal(err)
	}

	// Run gc in dry-run mode first — nothing removed.
	dryRun = true
	t.Cleanup(func() { dryRun = false })

	// Call cleanOldGenerations directly.
	removed := cleanOldGenerations(galeDir, storeRoot, 1, true)
	if removed != 2 {
		t.Errorf("dry-run: want 2 flagged, got %d", removed)
	}
	// All dirs still exist.
	for _, n := range []string{"1", "2", "3"} {
		if _, err := os.Stat(
			filepath.Join(genRoot, n),
		); err != nil {
			t.Errorf("dry-run: gen/%s should still exist", n)
		}
	}

	// Now run for real.
	dryRun = false
	removed = cleanOldGenerations(galeDir, storeRoot, 1, false)
	if removed != 2 {
		t.Errorf("want 2 removed, got %d", removed)
	}

	// gen/3 must survive, gen/1 and gen/2 must be gone.
	if _, err := os.Stat(
		filepath.Join(genRoot, "3"),
	); err != nil {
		t.Error("gen/3 should still exist")
	}
	for _, n := range []string{"1", "2"} {
		if _, err := os.Stat(
			filepath.Join(genRoot, n),
		); !os.IsNotExist(err) {
			t.Errorf("gen/%s should have been removed", n)
		}
	}
}

// TestGCSummaryDistinguishesVersionsAndGenerations
// verifies that the gc summary reports separate counts
// for package versions and generation directories
// rather than conflating them into a single counter.
func TestGCSummaryDistinguishesVersionsAndGenerations(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home) // isolate ~/.gale (gh#214)

	// keep = 1 so the fixture still has a generation to remove.
	// gc retains `[generation] keep` generations now (gh#247), and
	// under the default this two-generation fixture is entirely
	// retained — a summary of nothing says nothing about the two
	// counters this test is checking.
	if err := os.MkdirAll(filepath.Join(home, ".gale"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(home, ".gale", "config.toml"),
		[]byte("[generation]\nkeep = 1\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}

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
		storeRoot, "oldpkg", "0.1", "bin",
	)
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
		filepath.Join(galeDir, "current"),
	); err != nil {
		t.Fatal(err)
	}

	// Run gc in dry-run mode and capture stderr.
	chdirTo(t, projDir)

	dryRun = true
	t.Cleanup(func() { dryRun = false })

	var runErr error
	stderr := captureStderr(t, func() {
		runErr = gcCmd.RunE(gcCmd, nil)
	})

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
			0o755,
		); err != nil {
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

	resolver := recipeResolverFromMap(map[string]*recipe.Recipe{
		"postgresql": makeTestRecipe("postgresql", "17.2", 1,
			[]string{"readline"}, []string{"bison"}),
		"readline": makeTestRecipe("readline", "8.2", 2, nil, nil),
		"bison":    makeTestRecipe("bison", "3.8.2", 2, nil, nil),
	})

	ref, err := collectReferencedPackagesWithResolver(
		globalDir, "", s, resolver, nil,
	)
	if err != nil {
		t.Fatalf("collecting references: %v", err)
	}

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
			0o755,
		); err != nil {
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

	resolver := recipeResolverFromMap(map[string]*recipe.Recipe{
		"curl": makeTestRecipe("curl", "8.19.0", 1,
			[]string{"openssl"}, nil),
		"openssl": makeTestRecipe("openssl", "3.6.1", 2,
			[]string{"zlib"}, nil),
		"zlib": makeTestRecipe("zlib", "1.3.2", 2, nil, nil),
	})

	ref, err := collectReferencedPackagesWithResolver(
		globalDir, "", s, resolver, nil,
	)
	if err != nil {
		t.Fatalf("collecting references: %v", err)
	}

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
		0o755,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(
		filepath.Join(storeRoot, "openssl", "3.6.1-2", "lib"),
		0o755,
	); err != nil {
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

	ref, err := collectReferencedPackagesWithResolver(
		globalDir, "", s, nil, nil,
	)
	if err != nil {
		t.Fatalf("collecting references: %v", err)
	}

	if !ref["curl@8.19.0-1"] {
		t.Errorf("curl missing: %v", ref)
	}
	if ref["openssl@3.6.1-2"] {
		t.Errorf("openssl should not be referenced without " +
			"a resolver — falls back to config-only")
	}
}

// TestRemoveUnreferencedVersionsAllFailedReturnsFailureCount verifies
// that when every removal attempt fails, removeUnreferencedVersions
// returns a non-zero failure count and zero removed count. The gc
// early-return guard must check failedPkgs == 0 so it does not emit
// "Nothing to clean up." and return nil when all removals fail.
func TestRemoveUnreferencedVersionsAllFailedReturnsFailureCount(t *testing.T) {
	// Same setup as TestRemoveUnreferencedVersionsReturnsFailureCount:
	// one package, store root read-only → removal fails
	if os.Getuid() == 0 {
		t.Skip("root can remove read-only dirs")
	}
	tmp := t.TempDir()
	storeRoot := filepath.Join(tmp, "pkg")
	s := store.NewStore(storeRoot)

	pkgDir, err := s.Create("bat", "0.24.0")
	if err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(pkgDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Make the store root read-only so removal fails
	if err := os.Chmod(storeRoot, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(storeRoot, 0o755) })

	out := output.New(os.Stderr, false)
	removed, failed := removeUnreferencedVersions(s, map[string]bool{}, false, out)
	if failed == 0 {
		t.Error("expected failed > 0 when all removals fail")
	}
	if removed > 0 {
		t.Errorf("expected removed == 0, got %d", removed)
	}
	// The key invariant: when failed > 0 and removed == 0,
	// the caller MUST NOT say "Nothing to clean up." and must return an error.
	// (The early-return guard in gcCmd.RunE now checks failedPkgs == 0.)
	_ = removed
	_ = failed
}

// TestRemoveUnreferencedVersionsReturnsFailureCount verifies that
// removeUnreferencedVersions returns a non-zero failure count when
// a store removal fails (e.g. read-only directory). Without this,
// a partially-failed gc exits 0, silently leaving the store dirty.
func TestRemoveUnreferencedVersionsReturnsFailureCount(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root; chmod restrictions do not apply")
	}

	tmp := t.TempDir()
	storeRoot := filepath.Join(tmp, "pkg")

	// Create a package entry in the store.
	s := store.NewStore(storeRoot)
	pkgDir, err := s.Create("jq", "1.7.1")
	if err != nil {
		t.Fatal(err)
	}

	// Place a file inside so the package looks installed.
	if err := os.MkdirAll(filepath.Join(pkgDir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Make the version dir and its parent read-only so
	// os.RemoveAll will fail when gc tries to remove jq@1.7.1.
	if err := os.Chmod(pkgDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(pkgDir, 0o755) })

	nameDir := filepath.Join(storeRoot, "jq")
	if err := os.Chmod(nameDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(nameDir, 0o755) })

	out := output.New(os.Stderr, false)
	// Empty referenced set — jq@1.7.1 is unreferenced and
	// should be removed, but the read-only dirs will cause failure.
	_, failed := removeUnreferencedVersions(
		s, map[string]bool{}, false, out,
	)
	if failed == 0 {
		t.Error("expected failure count > 0 when store removal fails")
	}
}

// makeRegisteredProject creates a project dir with a gale.toml
// and an active generation (gen/1) whose bin/<binName> symlink
// points into storeRoot/<pkg>/<ver>/bin/<binName>. Returns the
// project path. Helper for the gh#115 registry retention tests.
func makeRegisteredProject(
	t *testing.T, storeRoot, configToml, pkg, ver, binName string,
) string {
	t.Helper()
	proj := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(proj, "gale.toml"),
		[]byte(configToml), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(proj, ".gale", "gen", "1", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Join(storeRoot, pkg, ver, "bin", binName),
		filepath.Join(binDir, binName),
	); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Join("gen", "1"),
		filepath.Join(proj, ".gale", "current"),
	); err != nil {
		t.Fatal(err)
	}
	return proj
}

// TestGCRetentionIncludesRegisteredProjects pins the gh#115
// fix: gc must retain store versions linked by the active
// generation of OTHER projects, discovered through the
// machine-local registry at <globalDir>/projects. The
// registered project's gen links jq@1.6 (which its config no
// longer mentions) — both the gen-linked and config-pinned
// versions must survive, while a fully unreferenced version
// must not.
func TestGCRetentionIncludesRegisteredProjects(t *testing.T) {
	storeRoot := t.TempDir()
	for _, d := range []struct{ n, v string }{
		{"jq", "1.6"}, {"jq", "1.7"}, {"fd", "9.0"},
	} {
		if err := os.MkdirAll(
			filepath.Join(storeRoot, d.n, d.v, "bin"), 0o755,
		); err != nil {
			t.Fatal(err)
		}
	}

	globalDir := t.TempDir()
	otherProj := makeRegisteredProject(
		t, storeRoot, "[packages]\njq = \"1.7\"\n",
		"jq", "1.6", "jq",
	)
	if err := os.WriteFile(
		filepath.Join(globalDir, "projects"),
		[]byte(otherProj+"\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	s := store.NewStore(storeRoot)
	ref, retained, err := collectGCRetention(
		globalDir, "", "", s, nil, nil,
		config.DefaultGenerationKeep,
	)
	if err != nil {
		t.Fatalf("collecting references: %v", err)
	}

	if !ref["jq@1.6"] {
		t.Error("jq@1.6 (linked by registered project's active " +
			"generation) must be retained")
	}
	if !ref["jq@1.7"] {
		t.Error("jq@1.7 (pinned by registered project's config) " +
			"must be retained")
	}
	if ref["fd@9.0"] {
		t.Error("fd@9.0 is unreferenced everywhere and must " +
			"not be retained")
	}
	if len(retained) != 1 || retained[0] != otherProj {
		t.Errorf("retained projects: want [%s], got %v",
			otherProj, retained)
	}
}

// TestGCRetentionSkipsVanishedRegisteredProjects verifies a
// registry entry whose gale.toml no longer exists contributes
// nothing and is not reported as contributing retention.
func TestGCRetentionSkipsVanishedRegisteredProjects(t *testing.T) {
	storeRoot := t.TempDir()
	globalDir := t.TempDir()
	ghost := t.TempDir() // registered but no gale.toml
	if err := os.WriteFile(
		filepath.Join(globalDir, "projects"),
		[]byte(ghost+"\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	s := store.NewStore(storeRoot)
	ref, retained, err := collectGCRetention(
		globalDir, "", "", s, nil, nil,
		config.DefaultGenerationKeep,
	)
	if err != nil {
		t.Fatalf("collecting references: %v", err)
	}
	if len(ref) != 0 {
		t.Errorf("vanished project must add no refs: %v", ref)
	}
	if len(retained) != 0 {
		t.Errorf("vanished project must not be listed as "+
			"contributing: %v", retained)
	}
}

// TestGCDryRunListsContributingProjects verifies `gale gc -n`
// names the registered projects whose generations contributed
// retention, and that a version linked only by another
// project's generation is not flagged for removal (gh#115).
func TestGCDryRunListsContributingProjects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GALE_OFFLINE", "1")
	t.Chdir(t.TempDir()) // neutral cwd: no project here

	storeRoot := filepath.Join(home, ".gale", "pkg")
	for _, d := range []struct{ n, v string }{
		{"jq", "1.7"}, {"fd", "9.0"},
	} {
		if err := os.MkdirAll(
			filepath.Join(storeRoot, d.n, d.v, "bin"), 0o755,
		); err != nil {
			t.Fatal(err)
		}
	}

	otherProj := makeRegisteredProject(
		t, storeRoot, "[packages]\njq = \"1.7\"\n",
		"jq", "1.7", "jq",
	)
	if err := os.WriteFile(
		filepath.Join(home, ".gale", "projects"),
		[]byte(otherProj+"\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	dryRun = true
	t.Cleanup(func() { dryRun = false })

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStderr := os.Stderr
	os.Stderr = w
	runErr := gcCmd.RunE(gcCmd, nil)
	w.Close()
	os.Stderr = origStderr

	data, _ := io.ReadAll(r)
	stderr := string(data)

	if runErr != nil {
		t.Fatalf("gc -n failed: %v\noutput: %s", runErr, stderr)
	}
	if !strings.Contains(stderr, otherProj) {
		t.Errorf("gc -n must name the contributing project %s, "+
			"got: %s", otherProj, stderr)
	}
	if strings.Contains(stderr, "Would remove jq@1.7") {
		t.Errorf("jq@1.7 is linked by a registered project's "+
			"generation and must not be removable: %s", stderr)
	}
	if !strings.Contains(stderr, "Would remove fd@9.0") {
		t.Errorf("fd@9.0 is unreferenced and should be flagged "+
			"for removal: %s", stderr)
	}
}

// TestGCPrunesStaleRegistry verifies a real (non-dry) gc run
// drops registry entries whose project no longer exists and
// keeps live ones (gh#115).
func TestGCPrunesStaleRegistry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GALE_OFFLINE", "1")
	t.Chdir(t.TempDir())

	live := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(live, "gale.toml"),
		[]byte("[packages]\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	ghost := t.TempDir() // no gale.toml

	galeDir := filepath.Join(home, ".gale")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(galeDir, "projects"),
		[]byte(live+"\n"+ghost+"\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	if err := gcCmd.RunE(gcCmd, nil); err != nil {
		t.Fatalf("gc failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(galeDir, "projects"))
	if err != nil {
		t.Fatalf("reading registry after gc: %v", err)
	}
	got := string(data)
	if strings.Contains(got, ghost) {
		t.Errorf("vanished project %s must be pruned from the "+
			"registry, got: %q", ghost, got)
	}
	if !strings.Contains(got, live) {
		t.Errorf("live project %s must survive prune, got: %q",
			live, got)
	}
}

// TestGCRealRunPreservesRegisteredProjectStoreDirs pins the
// end-to-end gh#115 guarantee on disk: a REAL (non-dry) gc run
// from a neutral cwd must not delete a store version that only
// a registered project's active generation links. The dry-run
// test above shares the retention set but exercises none of the
// real-mode-only code (projects.Prune before retention, the
// actual store.Remove), so a regression gating registered-
// project retention on dry-run would pass it — and reproduce
// the gen/222 incident. fd@9.0 doubles as the control: it is
// unreferenced everywhere and must actually be removed, proving
// the sweep ran.
func TestGCRealRunPreservesRegisteredProjectStoreDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GALE_OFFLINE", "1")
	t.Chdir(t.TempDir()) // neutral cwd: no project here

	storeRoot := filepath.Join(home, ".gale", "pkg")
	for _, d := range []struct{ n, v string }{
		{"jq", "1.7"}, {"fd", "9.0"},
	} {
		if err := os.MkdirAll(
			filepath.Join(storeRoot, d.n, d.v, "bin"), 0o755,
		); err != nil {
			t.Fatal(err)
		}
	}

	otherProj := makeRegisteredProject(
		t, storeRoot, "[packages]\njq = \"1.7\"\n",
		"jq", "1.7", "jq",
	)
	if err := os.WriteFile(
		filepath.Join(home, ".gale", "projects"),
		[]byte(otherProj+"\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	dryRun = false
	t.Cleanup(func() { dryRun = false })

	if err := gcCmd.RunE(gcCmd, nil); err != nil {
		t.Fatalf("gc failed: %v", err)
	}

	if _, err := os.Stat(
		filepath.Join(storeRoot, "jq", "1.7"),
	); err != nil {
		t.Errorf("jq@1.7 is linked by a registered project's "+
			"active generation and must survive a real gc: %v", err)
	}
	if _, err := os.Stat(
		filepath.Join(storeRoot, "fd", "9.0"),
	); !os.IsNotExist(err) {
		t.Errorf("fd@9.0 is unreferenced and must be removed by "+
			"a real gc (proves the sweep ran), err=%v", err)
	}
	// The live registered project must survive the pre-retention
	// registry prune.
	reg, err := os.ReadFile(filepath.Join(home, ".gale", "projects"))
	if err != nil {
		t.Fatalf("reading registry after gc: %v", err)
	}
	if !strings.Contains(string(reg), otherProj) {
		t.Errorf("live registered project must survive gc's "+
			"registry prune, got: %q", string(reg))
	}
}

// TestGCRealRunPreservesToolVersionsOnlyProjectPins pins the
// .tool-versions side of registered-project pin retention: a
// registered project that has NO gale.toml — it lives only via
// .tool-versions — must still contribute its pins to gc's
// retention set. The project pins jq 1.7 but its active
// generation links jq 1.6 (pin edited, sync not yet run), so
// only pin retention can keep jq@1.7 alive. gale.toml projects
// get this via mergeConfigAllHosts; .tool-versions projects must
// not be second-class. fd@9.0 is the unreferenced control.
func TestGCRealRunPreservesToolVersionsOnlyProjectPins(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GALE_OFFLINE", "1")
	t.Chdir(t.TempDir()) // neutral cwd: no project here

	storeRoot := filepath.Join(home, ".gale", "pkg")
	for _, d := range []struct{ n, v string }{
		{"jq", "1.6"}, {"jq", "1.7"}, {"fd", "9.0"},
	} {
		if err := os.MkdirAll(
			filepath.Join(storeRoot, d.n, d.v, "bin"), 0o755,
		); err != nil {
			t.Fatal(err)
		}
	}

	// Project with ONLY a .tool-versions (no gale.toml) pinning
	// jq 1.7, while its active generation links jq 1.6.
	proj := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(proj, ".tool-versions"),
		[]byte("jq 1.7\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(proj, ".gale", "gen", "1", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Join(storeRoot, "jq", "1.6", "bin", "jq"),
		filepath.Join(binDir, "jq"),
	); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Join("gen", "1"),
		filepath.Join(proj, ".gale", "current"),
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(home, ".gale", "projects"),
		[]byte(proj+"\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	dryRun = false
	t.Cleanup(func() { dryRun = false })

	if err := gcCmd.RunE(gcCmd, nil); err != nil {
		t.Fatalf("gc failed: %v", err)
	}

	if _, err := os.Stat(
		filepath.Join(storeRoot, "jq", "1.7"),
	); err != nil {
		t.Errorf("jq@1.7 is pinned by a registered project's "+
			".tool-versions and must survive a real gc: %v", err)
	}
	if _, err := os.Stat(
		filepath.Join(storeRoot, "jq", "1.6"),
	); err != nil {
		t.Errorf("jq@1.6 is linked by the registered project's "+
			"active generation and must survive a real gc: %v", err)
	}
	if _, err := os.Stat(
		filepath.Join(storeRoot, "fd", "9.0"),
	); !os.IsNotExist(err) {
		t.Errorf("fd@9.0 is unreferenced and must be removed by "+
			"a real gc (proves the sweep ran), err=%v", err)
	}
}

// TestGenerationLinksSupersededOrphanBarePin verifies gc rebuilds
// when a bare config pin shadows a superseded orphan on PATH.
func TestGenerationLinksSupersededOrphanBarePin(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()
	configPath := filepath.Join(galeDir, "gale.toml")

	orphan := mkStorePkg(t, storeRoot, "just", "1.48.0-2")
	mkActiveGen(t, galeDir, 1, filepath.Join(orphan, "bin", "just"))

	if err := os.WriteFile(configPath,
		[]byte("[packages]\njust = \"1.48.0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	pinResolve := versionedRecipeResolver(func(name, version string) (*recipe.Recipe, error) {
		if name != "just" || version != "1.48.0" {
			return nil, fmt.Errorf("unexpected pin %s@%s", name, version)
		}
		return &recipe.Recipe{
			Package: recipe.Package{
				Name: "just", Version: "1.48.0", Revision: 1,
			},
		}, nil
	})

	if !generationLinksSupersededOrphan(
		galeDir, storeRoot, configPath, pinResolve,
	) {
		t.Error("want true when bare pin shadows a superseded orphan")
	}
}

// TestGenerationLinksSupersededOrphanExplicitPin verifies gc does
// not rebuild when gale.toml explicitly pins the higher revision.
func TestGenerationLinksSupersededOrphanExplicitPin(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()
	configPath := filepath.Join(galeDir, "gale.toml")

	storeDir := mkStorePkg(t, storeRoot, "jq", "1.8.1-3")
	mkActiveGen(t, galeDir, 1, filepath.Join(storeDir, "bin", "jq"))

	if err := os.WriteFile(configPath,
		[]byte("[packages]\njq = \"1.8.1-3\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	pinResolve := versionedRecipeResolver(func(name, version string) (*recipe.Recipe, error) {
		if name != "jq" {
			return nil, fmt.Errorf("unexpected package %s", name)
		}
		return &recipe.Recipe{
			Package: recipe.Package{
				Name: "jq", Version: "1.8.1", Revision: 2,
			},
		}, nil
	})

	if generationLinksSupersededOrphan(
		galeDir, storeRoot, configPath, pinResolve,
	) {
		t.Error("want false when config explicitly pins the " +
			"superseded revision")
	}
}

// gcUnreadableProjectFixture builds the shared fixture for the
// gh#188 refusal tests: a temp HOME whose store holds jq@1.6,
// jq@1.7 and fd@9.0, plus one registered project pinning jq 1.7
// while its active generation links jq 1.6. Returns the store
// root and the project path. The caller breaks one reference
// source (config or generation) and runs gc from a neutral cwd.
func gcUnreadableProjectFixture(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GALE_OFFLINE", "1")
	t.Chdir(t.TempDir()) // neutral cwd: no project here

	storeRoot := filepath.Join(home, ".gale", "pkg")
	for _, d := range []struct{ n, v string }{
		{"jq", "1.6"}, {"jq", "1.7"}, {"fd", "9.0"},
	} {
		if err := os.MkdirAll(
			filepath.Join(storeRoot, d.n, d.v, "bin"), 0o755,
		); err != nil {
			t.Fatal(err)
		}
	}

	proj := makeRegisteredProject(
		t, storeRoot, "[packages]\njq = \"1.7\"\n",
		"jq", "1.6", "jq",
	)
	if err := os.WriteFile(
		filepath.Join(home, ".gale", "projects"),
		[]byte(proj+"\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	dryRun = false
	t.Cleanup(func() { dryRun = false })
	return storeRoot, proj
}

// breakGenerationWalk makes the generation tree under galeDir
// unwalkable while leaving its current symlink resolvable: the gen
// directory becomes a regular file, so the walk's root Lstat fails
// with ENOTDIR, and current still readlinks to "gen/1", which
// generation.Current parses without ever stating the directory.
//
// Structural, not a chmod: CI and the agent container run tests as
// root and bypass permission bits (gh#210).
//
// The current symlink is created when the scope has none, so a
// fixture that never built a generation can be broken the same way
// a synced one is.
func breakGenerationWalk(t *testing.T, galeDir string) {
	t.Helper()
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
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
}

// assertGCRefused checks that gc aborted with an error naming
// wantPath — the project whose references could not be read, or
// the store dir whose metadata could not be read — and that the
// whole store survived: neither the pinned version nor the
// unreferenced control was swept. fd@9.0 is the control — it is
// referenced by nothing, so its survival proves the sweep never
// ran rather than that retention happened to cover it.
func assertGCRefused(t *testing.T, err error, wantPath, storeRoot string) {
	t.Helper()
	if err == nil {
		t.Fatal("gc must refuse to sweep when a live project's " +
			"references cannot be read")
	}
	if !strings.Contains(err.Error(), wantPath) {
		t.Errorf("error must name %s, got: %v", wantPath, err)
	}
	for _, pkg := range []struct{ name, ver string }{
		{"jq", "1.7"}, {"fd", "9.0"},
	} {
		if _, sErr := os.Stat(
			filepath.Join(storeRoot, pkg.name, pkg.ver),
		); sErr != nil {
			t.Errorf("%s@%s must survive a refused gc: %v",
				pkg.name, pkg.ver, sErr)
		}
	}
}

// TestGCRefusesSweepWhenRegisteredProjectConfigUnreadable pins
// gh#188: a registered project whose gale.toml cannot be read is
// live, not gone, so gc has not proved its pins are unreferenced
// and must not sweep the store.
//
// The unreadable config is a directory at the gale.toml path, so
// os.ReadFile returns EISDIR — a non-ENOENT error, which is what
// a down network mount also produces. Permission bits would not
// work: CI and the agent container run tests as root and bypass
// them, so a chmod-based fixture passes for the wrong reason.
func TestGCRefusesSweepWhenRegisteredProjectConfigUnreadable(t *testing.T) {
	storeRoot, proj := gcUnreadableProjectFixture(t)

	cfgPath := filepath.Join(proj, "gale.toml")
	if err := os.Remove(cfgPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(cfgPath, 0o755); err != nil {
		t.Fatal(err)
	}

	assertGCRefused(t, gcCmd.RunE(gcCmd, nil), proj, storeRoot)
}

// TestGCRefusesSweepWhenProjectGenerationUnreadable pins the
// other half of gh#188: the project's config reads fine, but its
// active generation cannot be resolved, so everything the
// generation links is invisible to retention. An unresolvable
// current pointer is not proof of non-reference either.
func TestGCRefusesSweepWhenProjectGenerationUnreadable(t *testing.T) {
	storeRoot, proj := gcUnreadableProjectFixture(t)

	current := filepath.Join(proj, ".gale", "current")
	if err := os.Remove(current); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Join("gen", "bogus"), current,
	); err != nil {
		t.Fatal(err)
	}

	assertGCRefused(t, gcCmd.RunE(gcCmd, nil), proj, storeRoot)
}

// TestGCRefusesSweepWhenProjectGenerationWalkUnreadable extends
// gh#188's posture one layer down (gh#210). The current pointer
// resolves, so generation.Current succeeds, but the generation
// directory itself cannot be walked. The lenient reader answers
// with an empty map and no error, which retention reads as "this
// project links nothing" — the exact fail-open #188 removed from
// the layer above.
func TestGCRefusesSweepWhenProjectGenerationWalkUnreadable(t *testing.T) {
	storeRoot, proj := gcUnreadableProjectFixture(t)

	breakGenerationWalk(t, filepath.Join(proj, ".gale"))

	assertGCRefused(t, gcCmd.RunE(gcCmd, nil), proj, storeRoot)
}

// TestGCRefusesSweepWhenDepsMetadataUnreadable pins the third
// acceptance criterion of gh#210: .gale-deps.toml records the
// versions installed binaries actually link, which a recipe bump
// or an offline resolver miss leaves unprotected otherwise
// (gh#48). An unreadable one dropped its whole subtree from the
// retention closure with nothing but a warning on stderr.
//
// The metadata path is a directory, so os.ReadFile returns EISDIR
// — a non-ENOENT error, which absence must not be confused with:
// depsmeta.Read reports a missing file as an empty closure.
func TestGCRefusesSweepWhenDepsMetadataUnreadable(t *testing.T) {
	storeRoot, _ := gcUnreadableProjectFixture(t)

	// jq@1.7 is pinned by the registered project, so it is in the
	// retention set whose closure gets expanded.
	metaPath := filepath.Join(
		storeRoot, "jq", "1.7", depsmeta.File,
	)
	if err := os.Mkdir(metaPath, 0o755); err != nil {
		t.Fatal(err)
	}

	assertGCRefused(
		t, gcCmd.RunE(gcCmd, nil),
		filepath.Join(storeRoot, "jq", "1.7"), storeRoot,
	)
}

// TestGCForceSweepsDespiteUnreadableGenerationWalk verifies the
// gh#210 refusals reuse gh#188's one escape hatch rather than
// adding a second: --force restores the old lenient behavior for
// the generation walk too. fd@9.0 is unreferenced everywhere, so
// its removal proves the sweep ran.
func TestGCForceSweepsDespiteUnreadableGenerationWalk(t *testing.T) {
	storeRoot, proj := gcUnreadableProjectFixture(t)

	breakGenerationWalk(t, filepath.Join(proj, ".gale"))

	gcForce = true
	t.Cleanup(func() { gcForce = false })

	if err := gcCmd.RunE(gcCmd, nil); err != nil {
		t.Fatalf("gc --force must sweep anyway: %v", err)
	}
	if _, err := os.Stat(
		filepath.Join(storeRoot, "fd", "9.0"),
	); !os.IsNotExist(err) {
		t.Errorf("fd@9.0 is unreferenced and must be removed by "+
			"gc --force (proves the sweep ran), err=%v", err)
	}
}

// TestGCForceSweepsDespiteUnreadableProject verifies the escape
// hatch gh#188 asks for: --force restores the old behavior
// explicitly, so a user who knows the mount is gone for good can
// still reclaim space. fd@9.0 is unreferenced everywhere, so its
// removal proves the sweep ran rather than being skipped.
func TestGCForceSweepsDespiteUnreadableProject(t *testing.T) {
	storeRoot, proj := gcUnreadableProjectFixture(t)

	cfgPath := filepath.Join(proj, "gale.toml")
	if err := os.Remove(cfgPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(cfgPath, 0o755); err != nil {
		t.Fatal(err)
	}

	gcForce = true
	t.Cleanup(func() { gcForce = false })

	if err := gcCmd.RunE(gcCmd, nil); err != nil {
		t.Fatalf("gc --force must sweep anyway: %v", err)
	}
	if _, err := os.Stat(
		filepath.Join(storeRoot, "fd", "9.0"),
	); !os.IsNotExist(err) {
		t.Errorf("fd@9.0 is unreferenced and must be removed by "+
			"gc --force (proves the sweep ran), err=%v", err)
	}
}

// TestGCRetentionToleratesMissingConfigs guards the other half
// of the gh#188 split: a config that is absent references
// nothing, so retention must return a nil error for it. Making
// every read error fatal would turn gc into a no-op on a fresh
// install, where no gale.toml exists yet.
func TestGCRetentionToleratesMissingConfigs(t *testing.T) {
	globalDir := t.TempDir() // no gale.toml, no projects registry
	s := store.NewStore(t.TempDir())

	ref, retained, err := collectGCRetention(
		globalDir, "", "", s, nil, nil,
		config.DefaultGenerationKeep,
	)
	if err != nil {
		t.Fatalf("a missing config must not block the sweep: %v", err)
	}
	if len(ref) != 0 {
		t.Errorf("nothing is installed or pinned: %v", ref)
	}
	if len(retained) != 0 {
		t.Errorf("no project contributed retention: %v", retained)
	}
}

// TestGCRefusesSweepWhenRegisteredProjectConfigUnparsable
// extends gh#188 to the corrupt-config case: a gale.toml that
// reads fine but does not parse hides its pins exactly as an
// unreadable one does. A half-written config — an interrupted
// editor, a bad merge — is the likelier trigger of the two.
func TestGCRefusesSweepWhenRegisteredProjectConfigUnparsable(t *testing.T) {
	storeRoot, proj := gcUnreadableProjectFixture(t)

	if err := os.WriteFile(
		filepath.Join(proj, "gale.toml"),
		[]byte("[packages]\njq = \n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	assertGCRefused(t, gcCmd.RunE(gcCmd, nil), proj, storeRoot)
}
