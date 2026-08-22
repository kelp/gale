package installer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kelp/gale/internal/depsmeta"
	"github.com/kelp/gale/internal/recipe"
)

// IsStale reports whether an installed package is stale
// relative to the current recipes of its declared
// dependencies, or — for a working-tree recipe — relative
// to the recipe bytes recorded at install time (gh#265).
//
// goos and goarch identify the current platform (typically
// runtime.GOOS and runtime.GOARCH). They are passed to
// DependenciesForPlatform so that platform-scoped dep lists
// and constraints are included in the staleness check.
//
// A missing .gale-deps.toml causes IsStale to return
// true (stale) so a soft migration reinstalls old
// installs that predate this metadata.
//
// Only the Runtime deps declared on `r` (for the given
// platform) are considered: build-only tools (cmake, rust,
// go) and external system libraries are ignored, because
// the shipped binary cannot link them, so a bump to one
// must not force a re-download (gh#157).
// StaleQuery is IsStale's inputs. ctx stays a parameter so it is
// never stored.
type StaleQuery struct {
	StoreDir string
	Recipe   *recipe.Recipe
	GOOS     string
	GOARCH   string
	Resolver RecipeResolver
}

func IsStale(ctx context.Context, q StaleQuery) (bool, error) {
	storeDir, r, goos, goarch, resolver := q.StoreDir, q.Recipe, q.GOOS, q.GOARCH, q.Resolver
	// Check whether the metadata file is present before reading it.
	// A missing file means the package predates this metadata (soft
	// migration → stale). A present file with zero deps is a valid
	// zero-dep install (not stale).
	metaPath := filepath.Join(storeDir, depsmeta.File)
	if _, statErr := os.Stat(metaPath); os.IsNotExist(statErr) {
		return true, nil
	}

	if workingTreeRecipeStale(storeDir, r) {
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

	// Collect declared runtime deps only. Build-only deps are
	// deliberately excluded (gh#157): the shipped binary
	// links only its runtime closure. DependenciesForPlatform
	// merges the platform overlay so the check matches what
	// the builder recorded (runtimeDepsMetadata also resolves
	// via DependenciesForPlatform) and so platform-scoped
	// constraints are included in the staleness check.
	deps := r.DependenciesForPlatform(goos, goarch)
	declared := make([]string, 0, len(deps.Runtime))
	seen := make(map[string]bool)
	for _, dep := range deps.Runtime {
		if !seen[dep] {
			seen[dep] = true
			declared = append(declared, dep)
		}
	}

	// For each declared dep, resolve and compare.
	for _, name := range declared {
		resolved, err := resolver(ctx, name)
		if err != nil {
			return false, fmt.Errorf("resolve %s: %w", name, err)
		}
		if resolved == nil {
			return false, fmt.Errorf("no recipe found for %s", name)
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
		if expr, has := deps.Constraints[name]; has && expr != "" {
			c, cerr := recipe.ParseConstraint(expr)
			if cerr != nil {
				return false, fmt.Errorf(
					"parse constraint for %s: %w", name, cerr,
				)
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
