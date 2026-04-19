package installer

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/kelp/gale/internal/build"
	"github.com/kelp/gale/internal/depsmeta"
	"github.com/kelp/gale/internal/recipe"
)

// ResolvedDep records one dep's full identity at install
// time. Persisted in <storeDir>/.gale-deps.toml.
type ResolvedDep = depsmeta.ResolvedDep

// DepsMetadata is the on-disk form of a package's
// built-against dep closure.
type DepsMetadata = depsmeta.Metadata

// WriteDepsMetadata writes the metadata file into storeDir.
// Overwrites any existing file.
func WriteDepsMetadata(storeDir string, md DepsMetadata) error {
	return depsmeta.Write(storeDir, md)
}

// HasDepsMetadata reports whether <storeDir>/.gale-deps.toml
// exists. Callers use this to distinguish "old install that
// predates the revision system" (file missing → stale) from
// "fresh install with no deps" (file present but empty).
// Checking this before resolving a recipe lets sync and doctor
// detect soft-migration candidates even when the installed
// version is no longer in the registry's .versions index.
func HasDepsMetadata(storeDir string) bool {
	return depsmeta.Has(storeDir)
}

// ReadDepsMetadata reads <storeDir>/.gale-deps.toml.
// Returns an empty DepsMetadata (no error) if the file
// does not exist. Returns an error if the file exists
// but fails to parse.
func ReadDepsMetadata(storeDir string) (DepsMetadata, error) {
	return depsmeta.Read(storeDir)
}

// IsStale reports whether an installed package is stale
// relative to the current recipes of its declared
// dependencies. Stale means the package was built against
// a dep version-revision that differs from what the
// current recipe for that dep produces.
//
// A missing .gale-deps.toml causes IsStale to return
// true (stale) so a soft migration reinstalls old
// installs that predate this metadata.
//
// Only Runtime and Build deps declared on `r` are
// considered — external system libraries are ignored.
func IsStale(storeDir string, r *recipe.Recipe, resolver RecipeResolver) (bool, error) {
	// Check whether the metadata file is present before reading it.
	// A missing file means the package predates this metadata (soft
	// migration → stale). A present file with zero deps is a valid
	// zero-dep install (not stale).
	metaPath := filepath.Join(storeDir, depsmeta.File)
	if _, statErr := os.Stat(metaPath); os.IsNotExist(statErr) {
		return true, nil
	}

	md, err := depsmeta.Read(storeDir)
	if err != nil {
		return false, fmt.Errorf("read deps metadata: %w", err)
	}

	// Build a map from name to (version, revision) from metadata.
	type depKey struct {
		Version  string
		Revision int
	}
	metaMap := make(map[string]depKey, len(md.Deps))
	for _, dep := range md.Deps {
		metaMap[dep.Name] = depKey{Version: dep.Version, Revision: dep.Revision}
	}

	// Collect declared deps from Build and Runtime.
	declared := make([]string, 0, len(r.Dependencies.Build)+len(r.Dependencies.Runtime))
	seen := make(map[string]bool)
	for _, dep := range r.Dependencies.Build {
		if !seen[dep] {
			seen[dep] = true
			declared = append(declared, dep)
		}
	}
	for _, dep := range r.Dependencies.Runtime {
		if !seen[dep] {
			seen[dep] = true
			declared = append(declared, dep)
		}
	}

	// For each declared dep, resolve and compare.
	for _, name := range declared {
		resolved, err := resolver(name)
		if err != nil {
			return false, fmt.Errorf("resolve %s: %w", name, err)
		}

		current := depKey{
			Version:  resolved.Package.Version,
			Revision: resolved.Package.Revision,
		}

		recorded, ok := metaMap[name]
		if !ok {
			// Declared dep with no record in metadata —
			// stale regardless of constraint.
			return true, nil
		}

		// If the recipe declared an explicit version
		// constraint for this dep, use it to decide
		// staleness instead of exact match. This lets
		// recipe authors opt out of automatic
		// propagation for revisions that don't actually
		// affect them.
		if expr, has := r.Dependencies.Constraints[name]; has && expr != "" {
			c, cerr := recipe.ParseConstraint(expr)
			if cerr != nil {
				return false, fmt.Errorf(
					"parse constraint for %s: %w", name, cerr)
			}
			if !c.Satisfies(recorded.Version, recorded.Revision) {
				return true, nil
			}
			// Still also check that the current recipe's
			// dep also satisfies the constraint — if not,
			// we can't resolve this install anyway; treat
			// as stale so a reinstall can fail loudly.
			if !c.Satisfies(current.Version, current.Revision) {
				return true, nil
			}
			continue
		}

		if recorded != current {
			return true, nil
		}
	}

	return false, nil
}

// BuildDepsToResolved converts a BuildDeps into the flat
// (name, version, revision) list persisted in .gale-deps.toml.
// Thin wrapper around depsmeta.FromNamedDirs.
func BuildDepsToResolved(deps *build.BuildDeps) []ResolvedDep {
	if deps == nil {
		return nil
	}
	return depsmeta.FromNamedDirs(deps.NamedDirs)
}
