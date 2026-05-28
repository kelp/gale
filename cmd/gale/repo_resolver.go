package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/repo"
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
				"no resolver returned a result for %q", name,
			)
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

// tapFetcher refreshes a single tap by name. Production wraps
// repo.Manager.Fetch; tests substitute a recorder so they don't
// shell out to git.
type tapFetcher func(name string) error

// defaultTapFetcher builds a tapFetcher backed by a real
// repo.Manager rooted at ~/.gale/repos/. Pre-registers every
// supplied repo so subsequent Fetch calls resolve.
func defaultTapFetcher(repos []config.Repo) (tapFetcher, error) {
	galeDir, err := galeConfigDir()
	if err != nil {
		return nil, err
	}
	mgr := repo.NewManager(filepath.Join(galeDir, "repos"))
	for _, r := range repos {
		mgr.AddRepo(repo.RepoConfig{Name: r.Name, URL: r.URL})
	}
	return mgr.Fetch, nil
}

// liveTaps returns configured taps that have an existing cache
// directory. Mirrors the skip-missing-cache rule applied by
// configuredRepoResolvers — never-cloned taps have no clone to
// refresh.
func liveTaps() ([]config.Repo, error) {
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
	var live []config.Repo
	for _, r := range cfg.Repos {
		if _, err := os.Stat(filepath.Join(cacheRoot, r.Name)); err == nil {
			live = append(live, r)
		}
	}
	return live, nil
}

// refreshConfiguredTaps iterates every configured tap with an
// existing cache and calls fetch on it. Returns the number of
// taps attempted. Per-tap errors are warned but never bubbled —
// a stale tap cache must not block `gale update` (offline use,
// transient network).
//
// Emits no output when zero live taps exist so users without
// taps see no behavior change.
func refreshConfiguredTaps(out *output.Output, fetch tapFetcher) (int, error) {
	live, err := liveTaps()
	if err != nil {
		return 0, err
	}
	if len(live) == 0 {
		return 0, nil
	}
	out.Info(fmt.Sprintf("Refreshing %d tap(s)...", len(live)))
	for _, r := range live {
		if err := fetch(r.Name); err != nil {
			out.Warn(fmt.Sprintf("Refreshing %s: %v", r.Name, err))
		}
	}
	return len(live), nil
}

// refreshConfiguredTapsDefault is the production entry point —
// wires up a real git fetcher and refreshes every live tap.
// Safe to call when no taps are configured (no-ops silently).
func refreshConfiguredTapsDefault(out *output.Output) error {
	live, err := liveTaps()
	if err != nil {
		return err
	}
	if len(live) == 0 {
		return nil
	}
	fetch, err := defaultTapFetcher(live)
	if err != nil {
		return err
	}
	_, err = refreshConfiguredTaps(out, fetch)
	return err
}

// tapsOfflineMode reports whether tap auto-refresh should be
// suppressed. Triggers: an explicit --no-refresh flag from the
// caller, or `GALE_OFFLINE=1` in the environment. The env var
// uses a strict "1" match so unrelated values don't accidentally
// disable network access.
func tapsOfflineMode(noRefresh bool) bool {
	if noRefresh {
		return true
	}
	return os.Getenv("GALE_OFFLINE") == "1"
}
