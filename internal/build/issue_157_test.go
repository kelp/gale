package build

import (
	"runtime"
	"testing"

	"github.com/kelp/gale/internal/recipe"
)

// gh#157: runtimeDepsMetadata must record only the recipe's
// declared runtime deps, not the full build-environment
// closure. deps.NamedDirs holds every installed dep (build +
// runtime, transitive); recording all of it leaked build-only
// tools into .gale-deps.toml, causing spurious staleness and
// pinning them in the store.
func TestRuntimeDepsMetadataExcludesBuildClosure(t *testing.T) {
	r := &recipe.Recipe{
		Package: recipe.Package{Name: "fastfetch", Version: "2.0.0"},
		Dependencies: recipe.Dependencies{
			Build:   []string{"cmake", "pkgconf"},
			Runtime: []string{"zlib"},
		},
	}
	// The build environment installed cmake + pkgconf (build),
	// zlib (runtime), and openssl (transitive via cmake).
	deps := &BuildDeps{
		NamedDirs: map[string]string{
			"cmake":   "/store/cmake/3.0.0-2",
			"pkgconf": "/store/pkgconf/2.0.0-1",
			"openssl": "/store/openssl/3.0.0-1",
			"zlib":    "/store/zlib/1.3.1-1",
		},
	}

	got := runtimeDepsMetadata(r, deps)
	if len(got) != 1 {
		t.Fatalf("got %d deps %+v, want only the runtime dep zlib", len(got), got)
	}
	if got[0].Name != "zlib" {
		t.Fatalf("got dep %q, want zlib", got[0].Name)
	}
	if got[0].Version != "1.3.1" || got[0].Revision != 1 {
		t.Fatalf("got zlib %s-%d, want 1.3.1-1", got[0].Version, got[0].Revision)
	}
}

// A recipe with no runtime deps records an empty closure, even
// when build tools were installed.
func TestRuntimeDepsMetadataEmptyWhenNoRuntimeDeps(t *testing.T) {
	r := &recipe.Recipe{
		Package:      recipe.Package{Name: "ripgrep", Version: "14.0.0"},
		Dependencies: recipe.Dependencies{Build: []string{"rust"}},
	}
	deps := &BuildDeps{
		NamedDirs: map[string]string{"rust": "/store/rust/1.80.0-1"},
	}
	if got := runtimeDepsMetadata(r, deps); len(got) != 0 {
		t.Fatalf("got %+v, want no recorded deps", got)
	}
}

// Per-platform runtime deps are honored: the metadata reflects
// the runtime list for the host platform.
func TestRuntimeDepsMetadataUsesPlatformRuntimeDeps(t *testing.T) {
	key := runtime.GOOS + "-" + runtime.GOARCH
	r := &recipe.Recipe{
		Package: recipe.Package{Name: "tool", Version: "1.0.0"},
		Dependencies: recipe.Dependencies{
			Runtime: []string{"zlib"},
			Platform: map[string]recipe.PlatformDependencies{
				key: {Runtime: []string{"openssl"}},
			},
		},
	}
	deps := &BuildDeps{
		NamedDirs: map[string]string{
			"zlib":    "/store/zlib/1.3.1-1",
			"openssl": "/store/openssl/3.0.0-1",
		},
	}
	got := runtimeDepsMetadata(r, deps)
	if len(got) != 1 || got[0].Name != "openssl" {
		t.Fatalf("got %+v, want only openssl from the platform runtime list", got)
	}
}
