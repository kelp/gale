package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/recipe"
)

// composeResolvers returns a single RecipeResolver that consults
// each input resolver in turn and returns the first hit. If every
// resolver reports the package as missing, the error from the
// last resolver is returned — this is deliberate: configured
// repos run first as overrides, so the *fallback* resolver
// (typically the registry) carries the most informative error
// for a true cache-miss.
func composeResolvers(resolvers ...installer.RecipeResolver) installer.RecipeResolver {
	return func(name string) (*recipe.Recipe, error) {
		var lastErr error
		for _, r := range resolvers {
			rec, err := r(name)
			if err == nil {
				return rec, nil
			}
			lastErr = err
		}
		if lastErr == nil {
			return nil, fmt.Errorf(
				"no resolver returned a result for %q", name)
		}
		return nil, lastErr
	}
}

// configuredRepoResolvers builds a resolver per configured
// `[[repos]]` entry in `~/.gale/config.toml`, sorted by priority
// (lowest number first). Repos whose cache directory does not
// exist (never cloned, or cleared by `gale repo remove`) are
// skipped silently. Returns nil with nil error when no repos are
// configured or `config.toml` is absent — the install path then
// short-circuits to the registry-only resolver.
func configuredRepoResolvers() ([]installer.RecipeResolver, error) {
	cfg, err := loadAppConfig()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if len(cfg.Repos) == 0 {
		return nil, nil
	}
	galeDir, err := galeConfigDir()
	if err != nil {
		return nil, err
	}
	cacheRoot := filepath.Join(galeDir, "repos")

	sorted := make([]config.Repo, len(cfg.Repos))
	copy(sorted, cfg.Repos)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Priority < sorted[j].Priority
	})

	var resolvers []installer.RecipeResolver
	for _, r := range sorted {
		recipesDir := filepath.Join(cacheRoot, r.Name, "recipes")
		if _, err := os.Stat(recipesDir); err != nil {
			continue
		}
		resolvers = append(resolvers, localRecipeResolver(recipesDir))
	}
	return resolvers, nil
}
