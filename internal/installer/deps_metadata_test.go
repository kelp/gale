package installer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/build"
	"github.com/kelp/gale/internal/recipe"
)

// resolverFor returns a RecipeResolver backed by the provided map.
// Missing names produce a "recipe not found" error.
func resolverFor(m map[string]*recipe.Recipe) RecipeResolver {
	return func(name string) (*recipe.Recipe, error) {
		r, ok := m[name]
		if !ok {
			return nil, fmt.Errorf("recipe not found: %s", name)
		}
		return r, nil
	}
}

// curlRecipe builds a minimal curl recipe with the given version and revision.
func curlRecipe(version string, revision int) *recipe.Recipe {
	return &recipe.Recipe{
		Package: recipe.Package{Name: "curl", Version: version, Revision: revision},
	}
}

// Test 1: Write then Read round-trips a non-empty DepsMetadata.
func TestWriteReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := DepsMetadata{
		Deps: []ResolvedDep{
			{Name: "curl", Version: "8.19.0", Revision: 1},
			{Name: "zlib", Version: "1.3.1", Revision: 2},
		},
	}
	if err := WriteDepsMetadata(dir, want); err != nil {
		t.Fatalf("WriteDepsMetadata error: %v", err)
	}
	got, err := ReadDepsMetadata(dir)
	if err != nil {
		t.Fatalf("ReadDepsMetadata error: %v", err)
	}
	if len(got.Deps) != len(want.Deps) {
		t.Fatalf("got %d deps, want %d", len(got.Deps), len(want.Deps))
	}
	for i, dep := range want.Deps {
		g := got.Deps[i]
		if g.Name != dep.Name || g.Version != dep.Version || g.Revision != dep.Revision {
			t.Errorf("dep[%d]: got %+v, want %+v", i, g, dep)
		}
	}
}

// Test 3: Read returns error when file exists but is malformed.
func TestReadMalformedFileReturnsError(t *testing.T) {
	dir := t.TempDir()
	metaPath := filepath.Join(dir, ".gale-deps.toml")
	if err := os.WriteFile(metaPath, []byte("not valid toml {{{"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	_, err := ReadDepsMetadata(dir)
	if err == nil {
		t.Fatal("expected error for malformed TOML, got nil")
	}
}

// Test 4: IsStale returns true when metadata file is missing.
func TestIsStaleReturnsTrueWhenMetadataMissing(t *testing.T) {
	dir := t.TempDir()
	// No metadata file present.
	r := makeRecipe("mypkg", "1.0.0", nil, []string{"curl"})
	resolver := func(name string) (*recipe.Recipe, error) {
		return curlRecipe("8.19.0", 1), nil
	}
	stale, err := IsStale(dir, r, resolver)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !stale {
		t.Error("IsStale must return true when metadata file is missing")
	}
}

// Test 5: IsStale returns false when recorded deps match current recipes.
func TestIsStaleReturnsFalseWhenDepsMatch(t *testing.T) {
	dir := t.TempDir()
	md := DepsMetadata{
		Deps: []ResolvedDep{
			{Name: "curl", Version: "8.19.0", Revision: 1},
		},
	}
	if err := WriteDepsMetadata(dir, md); err != nil {
		t.Fatalf("setup: %v", err)
	}
	r := makeRecipe("mypkg", "1.0.0", nil, []string{"curl"})

	resolverCallCount := 0
	resolver := func(name string) (*recipe.Recipe, error) {
		resolverCallCount++
		return curlRecipe("8.19.0", 1), nil
	}

	stale, err := IsStale(dir, r, resolver)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stale {
		t.Error("IsStale must return false when recorded deps match current recipes")
	}
	if resolverCallCount == 0 {
		t.Error("IsStale must call the resolver at least once to check the dep")
	}
}

// Test 6: IsStale returns true when a dep's revision has bumped.
func TestIsStaleReturnsTrueWhenRevisionBumped(t *testing.T) {
	dir := t.TempDir()
	md := DepsMetadata{
		Deps: []ResolvedDep{
			{Name: "curl", Version: "8.19.0", Revision: 1},
		},
	}
	if err := WriteDepsMetadata(dir, md); err != nil {
		t.Fatalf("setup: %v", err)
	}
	r := makeRecipe("mypkg", "1.0.0", nil, []string{"curl"})
	resolver := resolverFor(map[string]*recipe.Recipe{
		"curl": curlRecipe("8.19.0", 2),
	})

	stale, err := IsStale(dir, r, resolver)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !stale {
		t.Error("IsStale must return true when dep revision has bumped from 1 to 2")
	}
}

// Test 7: IsStale returns true when a dep's version has bumped.
func TestIsStaleReturnsTrueWhenVersionBumped(t *testing.T) {
	dir := t.TempDir()
	md := DepsMetadata{
		Deps: []ResolvedDep{
			{Name: "curl", Version: "8.19.0", Revision: 1},
		},
	}
	if err := WriteDepsMetadata(dir, md); err != nil {
		t.Fatalf("setup: %v", err)
	}
	r := makeRecipe("mypkg", "1.0.0", nil, []string{"curl"})
	resolver := resolverFor(map[string]*recipe.Recipe{
		"curl": curlRecipe("8.20.0", 1),
	})

	stale, err := IsStale(dir, r, resolver)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !stale {
		t.Error("IsStale must return true when dep version has bumped from 8.19.0 to 8.20.0")
	}
}

// Test 8: IsStale returns the resolver's error when a dep cannot be resolved.
func TestIsStaleReturnsResolverError(t *testing.T) {
	dir := t.TempDir()
	md := DepsMetadata{
		Deps: []ResolvedDep{
			{Name: "curl", Version: "8.19.0", Revision: 1},
		},
	}
	if err := WriteDepsMetadata(dir, md); err != nil {
		t.Fatalf("setup: %v", err)
	}
	r := makeRecipe("mypkg", "1.0.0", nil, []string{"curl"})
	resolver := func(name string) (*recipe.Recipe, error) {
		return nil, fmt.Errorf("recipe not found: %s", name)
	}

	_, err := IsStale(dir, r, resolver)
	if err == nil {
		t.Fatal("IsStale must return a non-nil error when the resolver fails")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "curl") {
		t.Errorf("error should mention the dep name 'curl', got: %v", err)
	}
}

// Test 10: IsStale returns false for a package with zero declared deps when
// a valid (empty) metadata file is present. A package that genuinely has no
// deps is fresh — "file absent" (stale migration) must be distinguished from
// "file present with empty dep list" (fresh zero-dep package).
func TestIsStaleReturnsFalseForZeroDepPackage(t *testing.T) {
	storeDir := t.TempDir()

	// Write an empty metadata file (simulates a previous install of a zero-dep package).
	if err := WriteDepsMetadata(storeDir, DepsMetadata{}); err != nil {
		t.Fatalf("WriteDepsMetadata error: %v", err)
	}

	// Recipe with no declared deps.
	r := makeRecipe("mypkg", "1.0.0", nil, nil)

	// Resolver should never be called — there are no deps to resolve.
	resolver := func(name string) (*recipe.Recipe, error) {
		t.Errorf("resolver called unexpectedly for dep %q on a zero-dep package", name)
		return nil, fmt.Errorf("unexpected resolver call for %s", name)
	}

	stale, err := IsStale(storeDir, r, resolver)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stale {
		t.Error("IsStale must return false for a zero-dep package with a valid empty metadata file")
	}
}

// Test 9: IsStale ignores deps not declared in the current recipe.
// The resolver must be called for curl (the declared dep) and must NOT be
// called for openssl (an old dep no longer in the recipe). Calling openssl
// would return an error, so any implementation that iterates metadata deps
// instead of recipe deps will fail.
func TestIsStaleIgnoresUndeclaredDepsInMetadata(t *testing.T) {
	dir := t.TempDir()
	// Metadata has two entries: curl (current) and openssl (old, not in recipe).
	md := DepsMetadata{
		Deps: []ResolvedDep{
			{Name: "curl", Version: "8.19.0", Revision: 1},
			{Name: "openssl", Version: "3.0.0", Revision: 1},
		},
	}
	if err := WriteDepsMetadata(dir, md); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// Recipe only declares curl — openssl is no longer a dep.
	r := makeRecipe("mypkg", "1.0.0", nil, []string{"curl"})

	resolverCallNames := []string{}
	resolver := func(name string) (*recipe.Recipe, error) {
		resolverCallNames = append(resolverCallNames, name)
		if name == "curl" {
			return curlRecipe("8.19.0", 1), nil
		}
		return nil, fmt.Errorf("recipe not found: %s", name)
	}

	stale, err := IsStale(dir, r, resolver)
	if err != nil {
		t.Fatalf("unexpected error (openssl must not be resolved): %v", err)
	}
	if stale {
		t.Error("IsStale must return false — curl matches and openssl is not in the current recipe")
	}
	// The resolver must have been called for curl to verify it matches.
	curlChecked := false
	for _, name := range resolverCallNames {
		if name == "curl" {
			curlChecked = true
		}
		if name == "openssl" {
			t.Error("IsStale must not resolve deps not declared in the current recipe (called with openssl)")
		}
	}
	if !curlChecked {
		t.Error("IsStale must call the resolver for curl to verify it is up to date")
	}
}

// --- HasDepsMetadata tests ---

// Missing .gale-deps.toml file reports no metadata. This is the soft-migration
// signal: installs that predate the revision system carry no metadata file
// and should be flagged stale without needing to resolve their recipe.
func TestHasDepsMetadata_MissingFile(t *testing.T) {
	dir := t.TempDir()
	if HasDepsMetadata(dir) {
		t.Fatal("expected HasDepsMetadata=false when file is missing")
	}
}

// Present .gale-deps.toml file reports metadata exists, even when the file
// is empty (a valid zero-dep install). Pairs with the missing-file test so
// the bool distinguishes "missing" from "present-but-empty".
func TestHasDepsMetadata_PresentFile(t *testing.T) {
	dir := t.TempDir()
	if err := WriteDepsMetadata(dir, DepsMetadata{}); err != nil {
		t.Fatalf("WriteDepsMetadata: %v", err)
	}
	if !HasDepsMetadata(dir) {
		t.Fatal("expected HasDepsMetadata=true when file is present")
	}
}

// --- BuildDepsToResolved tests ---

// Behavior 1: Nil BuildDeps returns nil — verified by also checking that a
// non-nil input with one entry returns a non-nil slice, distinguishing the
// nil case from a broken implementation that always returns nil.
func TestBuildDepsToResolved_NilReturnsNil(t *testing.T) {
	// nil input must return nil
	got := BuildDepsToResolved(nil)
	if got != nil {
		t.Errorf("expected nil for nil input, got %v", got)
	}
	// non-nil input with one entry must return non-nil (ensures stub isn't hiding a bug)
	nonNil := &build.BuildDeps{
		NamedDirs: map[string]string{"curl": "/store/curl/8.0.0"},
	}
	gotNonNil := BuildDepsToResolved(nonNil)
	if gotNonNil == nil {
		t.Errorf("expected non-nil result for non-nil input with one dep, got nil")
	}
}

// Behavior 2: Empty NamedDirs (nil map) returns empty or nil slice, but a
// single-entry map returns a non-empty slice, proving the function reads NamedDirs.
func TestBuildDepsToResolved_EmptyNamedDirsNilMap(t *testing.T) {
	deps := &build.BuildDeps{NamedDirs: nil}
	got := BuildDepsToResolved(deps)
	if len(got) != 0 {
		t.Errorf("expected empty slice for nil NamedDirs, got %v", got)
	}
	// Confirm a non-empty map yields results (guards against always-empty stub).
	withOne := &build.BuildDeps{
		NamedDirs: map[string]string{"zlib": "/store/zlib/1.3.1"},
	}
	gotOne := BuildDepsToResolved(withOne)
	if len(gotOne) != 1 {
		t.Errorf("expected 1 dep for single-entry NamedDirs, got %d", len(gotOne))
	}
}

// Behavior 2 (variant): Empty NamedDirs (empty map) returns empty or nil slice.
func TestBuildDepsToResolved_EmptyNamedDirsEmptyMap(t *testing.T) {
	deps := &build.BuildDeps{NamedDirs: map[string]string{}}
	got := BuildDepsToResolved(deps)
	if len(got) != 0 {
		t.Errorf("expected empty slice for empty NamedDirs, got %v", got)
	}
	// Confirm a non-empty map yields results.
	withOne := &build.BuildDeps{
		NamedDirs: map[string]string{"openssl": "/store/openssl/3.0.0"},
	}
	gotOne := BuildDepsToResolved(withOne)
	if len(gotOne) != 1 {
		t.Errorf("expected 1 dep for single-entry NamedDirs, got %d", len(gotOne))
	}
}

// Behavior 3: <version>-<revision> basename parses into (name, version, revision).
func TestBuildDepsToResolved_VersionRevisionBasename(t *testing.T) {
	deps := &build.BuildDeps{
		NamedDirs: map[string]string{
			"curl": "/Users/x/.gale/pkg/curl/8.19.0-2",
		},
	}
	got := BuildDepsToResolved(deps)
	if len(got) != 1 {
		t.Fatalf("expected 1 dep, got %d: %v", len(got), got)
	}
	d := got[0]
	if d.Name != "curl" {
		t.Errorf("Name: got %q, want %q", d.Name, "curl")
	}
	if d.Version != "8.19.0" {
		t.Errorf("Version: got %q, want %q", d.Version, "8.19.0")
	}
	if d.Revision != 2 {
		t.Errorf("Revision: got %d, want %d", d.Revision, 2)
	}
}

// Behavior 4: Bare basename (no revision suffix) defaults revision to 1.
func TestBuildDepsToResolved_BareBasenameDefaultsRevisionToOne(t *testing.T) {
	deps := &build.BuildDeps{
		NamedDirs: map[string]string{
			"curl": "/Users/x/.gale/pkg/curl/8.19.0",
		},
	}
	got := BuildDepsToResolved(deps)
	if len(got) != 1 {
		t.Fatalf("expected 1 dep, got %d: %v", len(got), got)
	}
	d := got[0]
	if d.Version != "8.19.0" {
		t.Errorf("Version: got %q, want %q", d.Version, "8.19.0")
	}
	if d.Revision != 1 {
		t.Errorf("Revision: got %d, want 1 (default for bare basename)", d.Revision)
	}
}

// Behavior 5: Multi-digit revision is parsed correctly.
func TestBuildDepsToResolved_MultiDigitRevision(t *testing.T) {
	deps := &build.BuildDeps{
		NamedDirs: map[string]string{
			"curl": "/Users/x/.gale/pkg/curl/8.19.0-42",
		},
	}
	got := BuildDepsToResolved(deps)
	if len(got) != 1 {
		t.Fatalf("expected 1 dep, got %d: %v", len(got), got)
	}
	d := got[0]
	if d.Version != "8.19.0" {
		t.Errorf("Version: got %q, want %q", d.Version, "8.19.0")
	}
	if d.Revision != 42 {
		t.Errorf("Revision: got %d, want 42", d.Revision)
	}
}

// Behavior 6: Non-numeric suffix (pre-release like 1.0.0-rc1) treated as bare
// version — the whole basename is the version and revision defaults to 1.
func TestBuildDepsToResolved_NonNumericSuffixTreatedAsBareVersion(t *testing.T) {
	deps := &build.BuildDeps{
		NamedDirs: map[string]string{
			"foo": "/pkg/foo/1.0.0-rc1",
		},
	}
	got := BuildDepsToResolved(deps)
	if len(got) != 1 {
		t.Fatalf("expected 1 dep, got %d: %v", len(got), got)
	}
	d := got[0]
	if d.Name != "foo" {
		t.Errorf("Name: got %q, want %q", d.Name, "foo")
	}
	if d.Version != "1.0.0-rc1" {
		t.Errorf("Version: got %q, want %q", d.Version, "1.0.0-rc1")
	}
	if d.Revision != 1 {
		t.Errorf("Revision: got %d, want 1 (non-numeric suffix is not a revision)", d.Revision)
	}
}

// Behavior 7: Output is sorted by name for reproducibility.
func TestBuildDepsToResolved_OutputSortedByName(t *testing.T) {
	deps := &build.BuildDeps{
		NamedDirs: map[string]string{
			"zebra": "/store/zebra/1.0",
			"apple": "/store/apple/2.0",
			"mango": "/store/mango/3.0-1",
		},
	}
	got := BuildDepsToResolved(deps)
	if len(got) != 3 {
		t.Fatalf("expected 3 deps, got %d: %v", len(got), got)
	}
	wantOrder := []string{"apple", "mango", "zebra"}
	for i, want := range wantOrder {
		if got[i].Name != want {
			t.Errorf("dep[%d].Name: got %q, want %q (result must be sorted by name)", i, got[i].Name, want)
		}
	}
}
