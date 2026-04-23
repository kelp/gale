package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/store"
	"github.com/spf13/cobra"
)

var gcRecipes string

var gcCmd = &cobra.Command{
	Use:   "gc",
	Short: "Remove unused package versions from the store",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		out := newCmdOutput(cmd)

		// Resolve config paths.
		globalDir, _ := galeConfigDir()
		var projPath string
		if cwd, err := os.Getwd(); err == nil {
			projPath, _ = config.FindGaleConfig(cwd)
		}

		// Remove unreferenced package versions.
		storeRoot := defaultStoreRoot()
		s := store.NewStore(storeRoot)

		// Best-effort resolver so gc can expand config
		// packages' runtime deps. Build deps are reaped
		// intentionally. If the recipes repo isn't
		// available (nil resolver), gc falls back to
		// config-only retention.
		var resolver installer.RecipeResolver
		if ctx, cErr := newCmdContext(gcRecipes, false, false); cErr == nil {
			resolver = ctx.Resolver
		}

		// Collect all referenced name@version pairs.
		// Resolve through the store so bare config versions
		// match canonical revision dirs on disk, and include
		// runtime deps (not build deps) transitively.
		referenced := collectReferencedPackagesWithResolver(
			globalDir, projPath, s, resolver, out)
		removedPkgs := removeUnreferencedVersions(
			s, referenced, dryRun, out)

		// Clean up old generations.
		var removedGens int
		if globalDir != "" {
			removedGens += cleanOldGenerations(
				globalDir, dryRun)
		}
		if projPath != "" {
			projGaleDir := filepath.Join(
				filepath.Dir(projPath), ".gale")
			removedGens += cleanOldGenerations(
				projGaleDir, dryRun)
		}

		if removedPkgs == 0 && removedGens == 0 {
			out.Success("Nothing to clean up.")
			return nil
		}

		if dryRun {
			out.Info(fmt.Sprintf(
				"%d version(s) and %d generation(s) "+
					"would be removed",
				removedPkgs, removedGens))
			return nil
		}

		// Rebuild generation for the current scope.
		// If in a project, rebuild the project generation.
		// Always rebuild the global generation too.
		if projPath != "" {
			projRoot := filepath.Dir(projPath)
			projGaleDir := filepath.Join(projRoot, ".gale")
			if err := rebuildGeneration(projGaleDir,
				storeRoot, projPath); err != nil {
				return fmt.Errorf(
					"rebuild project generation: %w", err)
			}
		}
		if globalDir != "" {
			globalConfig := filepath.Join(
				globalDir, "gale.toml")
			if err := rebuildGeneration(globalDir,
				storeRoot, globalConfig); err != nil {
				return fmt.Errorf(
					"rebuild global generation: %w", err)
			}
		}

		out.Success(fmt.Sprintf(
			"Removed %d version(s) and %d generation(s)",
			removedPkgs, removedGens))
		return nil
	},
}

// isReferenced reports whether a store entry is kept by
// the config-derived reference set. The set is keyed on
// canonical name@version-<rev> strings produced by resolving
// each config entry through the store (see mergeConfig), so
// bare and revisioned config entries both end up comparing
// against the on-disk version key for exact match.
func isReferenced(name, version string, referenced map[string]bool) bool {
	return referenced[name+"@"+version]
}

// removeUnreferencedVersions iterates the store and
// removes any version not in the referenced set.
// Returns the number of versions removed (or flagged
// in dry-run mode).
func removeUnreferencedVersions(
	s *store.Store,
	referenced map[string]bool,
	dry bool,
	out *output.Output,
) int {
	installed, err := s.List()
	if err != nil {
		out.Warn(fmt.Sprintf("listing store: %v", err))
		return 0
	}
	var removed int
	for _, pkg := range installed {
		if isReferenced(pkg.Name, pkg.Version, referenced) {
			continue
		}
		if dry {
			out.Info(fmt.Sprintf(
				"Would remove %s@%s",
				pkg.Name, pkg.Version))
		} else {
			if err := s.Remove(
				pkg.Name, pkg.Version); err != nil {
				out.Warn(fmt.Sprintf(
					"Failed to remove %s@%s: %v",
					pkg.Name, pkg.Version, err))
				continue
			}
			out.Success(fmt.Sprintf(
				"Removed %s@%s",
				pkg.Name, pkg.Version))
		}
		removed++
	}
	return removed
}

// collectReferencedPackages is the config-only variant
// (no runtime-dep expansion). Kept for callers that don't
// have a recipe resolver available — e.g. doctor's orphan
// check, which would prefer the cheaper config-only view.
func collectReferencedPackages(
	globalDir, projPath string,
	s *store.Store, out *output.Output,
) map[string]bool {
	return collectReferencedPackagesWithResolver(
		globalDir, projPath, s, nil, out)
}

// collectReferencedPackagesWithResolver merges all
// name@version pairs from global and project configs into
// a set. When a resolver is provided, each config package's
// runtime deps (transitively) are also added so gc doesn't
// reap `readline@8.2-2` out from under a running postgres
// that links against it. Build deps are intentionally not
// expanded — users asked for a flat, no-sprawl store.
//
// Each entry is resolved through store.StorePath so bare
// versions (jq = "1.8.1") become canonical (jq@1.8.1-3)
// on disk and string compare cleanly against store.List
// output. Entries not in the store stay keyed on their
// raw name@version.
func collectReferencedPackagesWithResolver(
	globalDir, projPath string,
	s *store.Store,
	resolver installer.RecipeResolver,
	out *output.Output,
) map[string]bool {
	referenced := map[string]bool{}
	if globalDir != "" {
		mergeConfig(
			filepath.Join(globalDir, "gale.toml"),
			s, referenced, out)
	}
	if projPath != "" {
		mergeConfig(projPath, s, referenced, out)
	}
	if resolver != nil {
		expandRuntimeDeps(s, resolver, referenced)
	}
	return referenced
}

// mergeConfig reads a gale.toml and adds its packages
// to the referenced set. Silently skips missing files
// but warns on parse errors. Each entry is resolved via
// store.StorePath so the referenced key always matches
// the on-disk version name produced by store.List.
func mergeConfig(
	path string,
	s *store.Store,
	referenced map[string]bool,
	out *output.Output,
) {
	data, err := os.ReadFile(path)
	if err != nil {
		return // missing config is fine
	}
	cfg, err := config.ParseGaleConfig(string(data))
	if err != nil {
		out.Warn(fmt.Sprintf("parsing %s: %v", path, err))
		return
	}
	for name, version := range cfg.Packages {
		if dir, ok := s.StorePath(name, version); ok {
			referenced[name+"@"+filepath.Base(dir)] = true
			continue
		}
		// Not in the store — record the raw request so
		// callers that diff config-vs-installed can still
		// see the unresolved reference.
		referenced[name+"@"+version] = true
	}
}

// cleanOldGenerations removes all generation directories
// except the current one. Returns the count of generations
// removed (or flagged in dry-run mode).
func cleanOldGenerations(galeDir string, dry bool) int {
	out := newOutput()
	genRoot := filepath.Join(galeDir, "gen")
	entries, err := os.ReadDir(genRoot)
	if err != nil {
		return 0
	}
	curGen, _ := generation.Current(galeDir)
	var removed int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		n, err := strconv.Atoi(e.Name())
		if err != nil || n == curGen {
			continue
		}
		genPath := filepath.Join(genRoot, e.Name())
		if dry {
			out.Info(fmt.Sprintf(
				"Would remove generation %d", n))
		} else {
			if err := os.RemoveAll(genPath); err != nil {
				out.Warn(fmt.Sprintf(
					"Failed to remove generation %d: %v",
					n, err))
				continue
			}
			out.Success(fmt.Sprintf(
				"Removed generation %d", n))
		}
		removed++
	}
	return removed
}

// expandRuntimeDeps walks the runtime-dep closure of every
// package currently in `referenced` and adds each dep's
// on-disk canonical version to the set. Build deps are
// skipped by design — gc reaps them.
//
// Uses a breadth-first visit keyed on package name; a dep
// that's already been expanded is not re-visited, so cycles
// in the runtime-dep graph terminate. Resolver failures are
// tolerated: if a recipe can't be found, the walk just
// stops at that node rather than aborting gc entirely.
func expandRuntimeDeps(
	s *store.Store,
	resolver installer.RecipeResolver,
	referenced map[string]bool,
) {
	// Seed the queue with every package name already in
	// the set. The set is keyed name@version, so split.
	queue := make([]string, 0, len(referenced))
	visited := make(map[string]bool, len(referenced))
	for key := range referenced {
		at := lastIndex(key, '@')
		if at < 0 {
			continue
		}
		name := key[:at]
		if !visited[name] {
			visited[name] = true
			queue = append(queue, name)
		}
	}

	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]

		r, err := resolver(name)
		if err != nil || r == nil {
			continue // missing recipe — can't expand; skip.
		}

		for _, dep := range r.Dependencies.Runtime {
			if visited[dep] {
				continue
			}
			visited[dep] = true
			queue = append(queue, dep)

			// Best-effort: if the dep isn't in the store,
			// nothing to add. The recipe is the source of
			// truth for the dep name but the store decides
			// which revision.
			depRecipe, rErr := resolver(dep)
			version := ""
			if rErr == nil && depRecipe != nil {
				version = depRecipe.Package.Version
			}
			if version == "" {
				continue
			}
			if dir, ok := s.StorePath(dep, version); ok {
				referenced[dep+"@"+filepath.Base(dir)] = true
			}
		}
	}
}

// lastIndex returns the position of the last occurrence of c
// in s, or -1 if absent. Local helper so the runtime-dep walk
// doesn't need to import strings just for IndexByte.
func lastIndex(s string, c byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == c {
			return i
		}
	}
	return -1
}

func init() {
	gcCmd.Flags().StringVar(&gcRecipes, "recipes", "",
		"Use local recipes directory (default: ../gale-recipes/) "+
			"for runtime-dep retention")
	gcCmd.Flags().Lookup("recipes").NoOptDefVal = "auto"
	rootCmd.AddCommand(gcCmd)
}
