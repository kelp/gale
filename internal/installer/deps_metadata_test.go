package installer

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/depsmeta"
	"github.com/kelp/gale/internal/recipe"
)

// resolverFor returns a RecipeResolver backed by the provided map.
// Missing names produce a "recipe not found" error.
func resolverFor(m map[string]*recipe.Recipe) RecipeResolver {
	return func(_ context.Context, name string) (*recipe.Recipe, error) {
		r, ok := m[name]
		if !ok {
			return nil, fmt.Errorf("recipe not found: %s", name)
		}
		return r, nil
	}
}

func makeRecipe(runtimeDeps []string) *recipe.Recipe {
	return &recipe.Recipe{
		Package: recipe.Package{Name: "mypkg", Version: "1.0.0"},
		Dependencies: recipe.Dependencies{
			Runtime: runtimeDeps,
		},
	}
}

// curlRecipe builds a minimal curl recipe with the given version and revision.
func curlRecipe(version string, revision int) *recipe.Recipe {
	return &recipe.Recipe{
		Package: recipe.Package{Name: "curl", Version: version, Revision: revision},
	}
}

// Test 4: IsStale returns true when metadata file is missing.
func TestIsStaleReturnsTrueWhenMetadataMissing(t *testing.T) {
	dir := t.TempDir()
	// No metadata file present.
	r := makeRecipe([]string{"curl"})
	resolver := func(_ context.Context, name string) (*recipe.Recipe, error) {
		return curlRecipe("8.19.0", 1), nil
	}
	stale, err := IsStale(context.Background(), StaleQuery{StoreDir: dir, Recipe: r, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Resolver: resolver})
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
	md := depsmeta.Metadata{
		Deps: []depsmeta.ResolvedDep{
			{Name: "curl", Version: "8.19.0", Revision: 1},
		},
	}
	if err := depsmeta.Write(dir, md); err != nil {
		t.Fatalf("setup: %v", err)
	}
	r := makeRecipe([]string{"curl"})

	resolverCallCount := 0
	resolver := func(_ context.Context, name string) (*recipe.Recipe, error) {
		resolverCallCount++
		return curlRecipe("8.19.0", 1), nil
	}

	stale, err := IsStale(context.Background(), StaleQuery{StoreDir: dir, Recipe: r, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Resolver: resolver})
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
	md := depsmeta.Metadata{
		Deps: []depsmeta.ResolvedDep{
			{Name: "curl", Version: "8.19.0", Revision: 1},
		},
	}
	if err := depsmeta.Write(dir, md); err != nil {
		t.Fatalf("setup: %v", err)
	}
	r := makeRecipe([]string{"curl"})
	resolver := resolverFor(map[string]*recipe.Recipe{
		"curl": curlRecipe("8.19.0", 2),
	})

	stale, err := IsStale(context.Background(), StaleQuery{StoreDir: dir, Recipe: r, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Resolver: resolver})
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
	md := depsmeta.Metadata{
		Deps: []depsmeta.ResolvedDep{
			{Name: "curl", Version: "8.19.0", Revision: 1},
		},
	}
	if err := depsmeta.Write(dir, md); err != nil {
		t.Fatalf("setup: %v", err)
	}
	r := makeRecipe([]string{"curl"})
	resolver := resolverFor(map[string]*recipe.Recipe{
		"curl": curlRecipe("8.20.0", 1),
	})

	stale, err := IsStale(context.Background(), StaleQuery{StoreDir: dir, Recipe: r, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Resolver: resolver})
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
	md := depsmeta.Metadata{
		Deps: []depsmeta.ResolvedDep{
			{Name: "curl", Version: "8.19.0", Revision: 1},
		},
	}
	if err := depsmeta.Write(dir, md); err != nil {
		t.Fatalf("setup: %v", err)
	}
	r := makeRecipe([]string{"curl"})
	resolver := func(_ context.Context, name string) (*recipe.Recipe, error) {
		return nil, fmt.Errorf("recipe not found: %s", name)
	}

	_, err := IsStale(context.Background(), StaleQuery{StoreDir: dir, Recipe: r, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Resolver: resolver})
	if err == nil {
		t.Fatal("IsStale must return a non-nil error when the resolver fails")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "curl") {
		t.Errorf("error should mention the dep name 'curl', got: %v", err)
	}
}

// Test 10: IsStale returns false for a package with zero declared deps when
// a valid (empty) metadata file is present.
func TestIsStaleReturnsFalseForZeroDepPackage(t *testing.T) {
	storeDir := t.TempDir()

	// Write an empty metadata file (simulates a previous install of a zero-dep package).
	if err := depsmeta.Write(storeDir, depsmeta.Metadata{}); err != nil {
		t.Fatalf("depsmeta.Write error: %v", err)
	}

	// Recipe with no declared deps.
	r := makeRecipe(nil)

	// Resolver should never be called — there are no deps to resolve.
	resolver := func(_ context.Context, name string) (*recipe.Recipe, error) {
		t.Errorf("resolver called unexpectedly for dep %q on a zero-dep package", name)
		return nil, fmt.Errorf("unexpected resolver call for %s", name)
	}

	stale, err := IsStale(context.Background(), StaleQuery{StoreDir: storeDir, Recipe: r, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Resolver: resolver})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stale {
		t.Error("IsStale must return false for a zero-dep package with a valid empty metadata file")
	}
}

// Test 9: IsStale ignores deps not declared in the current recipe.
func TestIsStaleIgnoresUndeclaredDepsInMetadata(t *testing.T) {
	dir := t.TempDir()
	// Metadata has two entries: curl (current) and openssl (old, not in recipe).
	md := depsmeta.Metadata{
		Deps: []depsmeta.ResolvedDep{
			{Name: "curl", Version: "8.19.0", Revision: 1},
			{Name: "openssl", Version: "3.0.0", Revision: 1},
		},
	}
	if err := depsmeta.Write(dir, md); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// Recipe only declares curl — openssl is no longer a dep.
	r := makeRecipe([]string{"curl"})

	resolverCallNames := []string{}
	resolver := func(_ context.Context, name string) (*recipe.Recipe, error) {
		resolverCallNames = append(resolverCallNames, name)
		if name == "curl" {
			return curlRecipe("8.19.0", 1), nil
		}
		return nil, fmt.Errorf("recipe not found: %s", name)
	}

	stale, err := IsStale(context.Background(), StaleQuery{StoreDir: dir, Recipe: r, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Resolver: resolver})
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

// TestIsStaleWithPlatformScopedConstraintViolated tests that IsStale
// returns true when a platform-scoped constraint is violated. Before
// option (a) was fully applied, IsStale read r.Dependencies.Constraints
// directly and never saw constraints scoped to a platform entry.
// This test ensures that DependenciesForPlatform is consulted, so
// platform-scoped constraints are visible to the staleness check.
func TestIsStaleWithPlatformScopedConstraintViolated(t *testing.T) {
	dir := t.TempDir()
	// expat was recorded at 2.7.4 (violates >=2.7.5-2 constraint).
	md := depsmeta.Metadata{
		Deps: []depsmeta.ResolvedDep{
			{Name: "expat", Version: "2.7.4", Revision: 1},
		},
	}
	if err := depsmeta.Write(dir, md); err != nil {
		t.Fatalf("depsmeta.Write error: %v", err)
	}

	// Build a recipe that has expat only as a platform-scoped dep
	// with a minimum version constraint.
	r := &recipe.Recipe{
		Package: recipe.Package{Name: "mypkg", Version: "1.0.0"},
		Dependencies: recipe.Dependencies{
			Platform: map[string]recipe.PlatformDependencies{
				"linux-amd64": {
					Runtime:     []string{"expat"},
					Constraints: map[string]string{"expat": ">=2.7.5-2"},
				},
			},
		},
	}

	resolver := func(_ context.Context, name string) (*recipe.Recipe, error) {
		return &recipe.Recipe{
			Package: recipe.Package{Name: name, Version: "2.7.6", Revision: 1},
		}, nil
	}

	// IsStale must return true: the recorded version (2.7.4) violates
	// the platform-scoped constraint (>=2.7.5-2). Without calling
	// DependenciesForPlatform, IsStale would see no constraint and no
	// declared dep, and return false — a silent miss.
	stale, err := IsStale(context.Background(), StaleQuery{StoreDir: dir, Recipe: r, GOOS: "linux", GOARCH: "amd64", Resolver: resolver})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !stale {
		t.Error("IsStale must return true when platform-scoped constraint is violated")
	}
}

// TestIsStaleNilResolverReturn: resolver returns (nil, nil) for a
// declared dep; IsStale must return a non-nil error (not panic).
func TestIsStaleNilResolverReturn(t *testing.T) {
	dir := t.TempDir()
	md := depsmeta.Metadata{
		Deps: []depsmeta.ResolvedDep{
			{Name: "curl", Version: "8.19.0", Revision: 1},
		},
	}
	if err := depsmeta.Write(dir, md); err != nil {
		t.Fatalf("setup: %v", err)
	}
	r := makeRecipe([]string{"curl"})
	// Resolver returns nil recipe with no error — the contract described
	// in installer.go:30 ("Returns nil if the package has no recipe").
	resolver := func(_ context.Context, name string) (*recipe.Recipe, error) {
		return nil, nil //nolint:nilnil // deliberate: exercises the (nil, nil) resolver contract
	}

	_, err := IsStale(context.Background(), StaleQuery{StoreDir: dir, Recipe: r, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Resolver: resolver})
	if err == nil {
		t.Fatal("IsStale must return a non-nil error when resolver returns (nil, nil)")
	}
	if !strings.Contains(err.Error(), "curl") {
		t.Errorf("error should mention the dep name 'curl', got: %v", err)
	}
}
