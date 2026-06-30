package main

import (
	"fmt"
	"io"
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
		"[packages]\njq = \"1.7\"\n",
	),
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

	ref := collectReferencedPackages(globalDir, "", s, out)
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

	ref := collectReferencedPackagesAllHosts(globalDir, "", s, pinResolve, out)
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
	removed := cleanOldGenerations(galeDir, storeRoot, true)
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
	removed = cleanOldGenerations(galeDir, storeRoot, false)
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
	out := output.New(os.Stderr, false)

	resolver := recipeResolverFromMap(map[string]*recipe.Recipe{
		"postgresql": makeTestRecipe("postgresql", "17.2", 1,
			[]string{"readline"}, []string{"bison"}),
		"readline": makeTestRecipe("readline", "8.2", 2, nil, nil),
		"bison":    makeTestRecipe("bison", "3.8.2", 2, nil, nil),
	})

	ref := collectReferencedPackagesWithResolver(
		globalDir, "", s, resolver, nil, out,
	)

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
	out := output.New(os.Stderr, false)

	resolver := recipeResolverFromMap(map[string]*recipe.Recipe{
		"curl": makeTestRecipe("curl", "8.19.0", 1,
			[]string{"openssl"}, nil),
		"openssl": makeTestRecipe("openssl", "3.6.1", 2,
			[]string{"zlib"}, nil),
		"zlib": makeTestRecipe("zlib", "1.3.2", 2, nil, nil),
	})

	ref := collectReferencedPackagesWithResolver(
		globalDir, "", s, resolver, nil, out,
	)

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
	out := output.New(os.Stderr, false)

	ref := collectReferencedPackagesWithResolver(
		globalDir, "", s, nil, nil, out,
	)

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
	out := output.New(os.Stderr, false)
	ref, retained := collectGCRetention(
		globalDir, "", "", s, nil, nil, out,
	)

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
	out := output.New(os.Stderr, false)
	ref, retained := collectGCRetention(
		globalDir, "", "", s, nil, nil, out,
	)
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
