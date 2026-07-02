package installer

import (
	"runtime"
	"testing"

	"github.com/kelp/gale/internal/recipe"
)

// gh#157: the build-environment closure leaks into
// .gale-deps.toml, so a bump to a build-only dependency
// (cmake, rust, go) marks a package stale and forces a
// re-download even though the shipped binary cannot link
// that tool. IsStale must consider only runtime deps.

// TestIsStaleIgnoresBuildDepBump: fastfetch declares cmake as
// a build-only dep and no runtime deps. A cmake revision bump
// must NOT mark fastfetch stale. Pre-fix IsStale checks build
// deps too, so it wrongly reports stale.
func TestIsStaleIgnoresBuildDepBump(t *testing.T) {
	dir := t.TempDir()
	// Metadata as written today: the build closure leaked in,
	// recording cmake at the revision it was built against.
	md := DepsMetadata{
		Deps: []ResolvedDep{
			{Name: "cmake", Version: "3.0.0", Revision: 1},
		},
	}
	if err := WriteDepsMetadata(dir, md); err != nil {
		t.Fatalf("WriteDepsMetadata error: %v", err)
	}
	r := &recipe.Recipe{
		Package:      recipe.Package{Name: "fastfetch", Version: "2.0.0", Revision: 1},
		Dependencies: recipe.Dependencies{Build: []string{"cmake"}},
	}
	// cmake's recipe revision bumped to 2.
	resolver := resolverFor(map[string]*recipe.Recipe{
		"cmake": {Package: recipe.Package{Name: "cmake", Version: "3.0.0", Revision: 2}},
	})

	stale, err := IsStale(dir, r, resolver)
	if err != nil {
		t.Fatalf("IsStale error: %v", err)
	}
	if stale {
		t.Fatal("fastfetch marked stale after a build-only cmake bump; " +
			"build deps must not affect staleness")
	}
}

// TestIsStaleStillDetectsRuntimeDepBump: a runtime dep bump
// must still mark the package stale, so the fix does not
// silence legitimate propagation.
func TestIsStaleStillDetectsRuntimeDepBump(t *testing.T) {
	dir := t.TempDir()
	md := DepsMetadata{
		Deps: []ResolvedDep{
			{Name: "zlib", Version: "1.3.1", Revision: 1},
		},
	}
	if err := WriteDepsMetadata(dir, md); err != nil {
		t.Fatalf("WriteDepsMetadata error: %v", err)
	}
	r := &recipe.Recipe{
		Package:      recipe.Package{Name: "curl", Version: "8.0.0", Revision: 1},
		Dependencies: recipe.Dependencies{Runtime: []string{"zlib"}},
	}
	resolver := resolverFor(map[string]*recipe.Recipe{
		"zlib": {Package: recipe.Package{Name: "zlib", Version: "1.3.1", Revision: 2}},
	})

	stale, err := IsStale(dir, r, resolver)
	if err != nil {
		t.Fatalf("IsStale error: %v", err)
	}
	if !stale {
		t.Fatal("curl not marked stale after a runtime zlib bump")
	}
}

// TestIsStaleHonorsPlatformRuntimeOverride: when a recipe
// overrides its runtime deps for the host platform, IsStale
// must check the platform-overlaid runtime list — the same
// list the builder records via DependenciesForPlatform.
// Checking the base list instead leaves the package
// permanently stale: the recorded platform dep is treated as
// "declared but unrecorded" for the base dep that was never
// installed. See gh#157 (writer/checker symmetry).
func TestIsStaleHonorsPlatformRuntimeOverride(t *testing.T) {
	dir := t.TempDir()
	// Metadata records what the builder wrote: the platform
	// runtime dep (openssl), resolved to its installed rev.
	md := DepsMetadata{
		Deps: []ResolvedDep{
			{Name: "openssl", Version: "3.0.0", Revision: 1},
		},
	}
	if err := WriteDepsMetadata(dir, md); err != nil {
		t.Fatalf("WriteDepsMetadata error: %v", err)
	}
	key := runtime.GOOS + "-" + runtime.GOARCH
	r := &recipe.Recipe{
		Package: recipe.Package{Name: "tool", Version: "1.0.0", Revision: 1},
		Dependencies: recipe.Dependencies{
			// Base declares zlib, but the platform overlay
			// replaces it with openssl — what actually links.
			Runtime: []string{"zlib"},
			Platform: map[string]recipe.PlatformDependencies{
				key: {Runtime: []string{"openssl"}},
			},
		},
	}
	resolver := resolverFor(map[string]*recipe.Recipe{
		"openssl": {Package: recipe.Package{Name: "openssl", Version: "3.0.0", Revision: 1}},
		"zlib":    {Package: recipe.Package{Name: "zlib", Version: "1.3.1", Revision: 1}},
	})

	stale, err := IsStale(dir, r, resolver)
	if err != nil {
		t.Fatalf("IsStale error: %v", err)
	}
	if stale {
		t.Fatal("tool marked stale despite the platform " +
			"runtime dep (openssl) matching its recorded " +
			"revision; IsStale must use the platform overlay")
	}
}
