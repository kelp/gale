// Package prewarm warms the registry's ETag cache for a set
// of dependency names by resolving them concurrently before
// the serial InstallBuildDeps walk. Errors are swallowed:
// the goal is cache pre-population, not error gating.
package prewarm

import (
	"context"

	"github.com/kelp/gale/internal/parallel"
	"github.com/kelp/gale/internal/recipe"
)

// prewarmWorkers matches the pool size used by sync, outdated,
// and sbom. Per-package work is HTTP-bound; more parallelism
// wouldn't help for typical dep counts (well under 8) and would
// just add goroutine overhead.
const prewarmWorkers = 8

// RecipeResolver finds and parses a recipe by package name.
// Identical underlying type to installer.RecipeResolver; both
// are func(string) (*recipe.Recipe, error), so values of either
// type are directly assignable here without a conversion.
type RecipeResolver = func(string) (*recipe.Recipe, error)

// PrewarmRecipeDeps fetches each dep concurrently via resolver
// to populate the registry's ETag cache before the serial
// InstallBuildDeps walk consults it. Fire-and-forget: errors are
// swallowed so transient registry hiccups don't gate the install
// — the real fetch in InstallBuildDeps will surface persistent
// failures.
func PrewarmRecipeDeps(ctx context.Context, deps []string, resolver RecipeResolver) {
	if len(deps) == 0 || resolver == nil {
		return
	}
	_ = parallel.ForEach(ctx, deps, prewarmWorkers,
		func(_ context.Context, dep string) error {
			_, _ = resolver(dep)
			return nil
		})
}
