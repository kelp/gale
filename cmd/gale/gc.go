package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kelp/gale/internal/build"
	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/filelock"
	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/store"
	"github.com/spf13/cobra"
)

var gcRecipes string

var gcCmd = &cobra.Command{
	Use:   "gc",
	Short: "Remove unused package versions and old generations",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		out := newCmdOutput(cmd)

		// Resolve config paths. The project gale dir is derived
		// through galeDirForConfig so a config found inside
		// ~/.gale/ maps to the global scope instead of a bogus
		// nested ~/.gale/.gale dir (gh#96 site).
		globalDir, _ := galeConfigDir()
		var projPath, projGaleDir string
		if cwd, err := os.Getwd(); err == nil {
			projPath, _ = config.FindGaleConfig(cwd)
		}
		if projPath != "" {
			dir, err := galeDirForConfig(projPath)
			if err != nil || dir == globalDir {
				// The "project" config is the global one —
				// the global pass below already covers it.
				projPath = ""
			} else {
				projGaleDir = dir
			}
		}

		// Remove unreferenced package versions.
		storeRoot := defaultStoreRoot()
		s := store.NewStore(storeRoot)

		// Best-effort resolver so gc can top up retention with
		// registry-resolved runtime deps. Build deps are reaped
		// intentionally. If the recipes repo isn't available
		// (nil resolver), the installed .gale-deps.toml walk in
		// collectGCRetention still protects recorded deps.
		var resolver installer.RecipeResolver
		if ctx, cErr := newCmdContext(gcRecipes, false, false); cErr == nil {
			resolver = ctx.Resolver
		}

		// Retention covers config pins across ALL hosts,
		// everything the active generations link, and each
		// retained package's installed dep closure. Anything
		// retained can never be deleted below, so the active
		// generation needs no rebuild after gc — which also
		// means a `gale generations rollback` survives gc
		// instead of being silently re-advanced to config
		// state (gh#46, gh#47).
		referenced := collectGCRetention(
			globalDir, projPath, projGaleDir, s, resolver, out,
		)
		removedPkgs, failedPkgs := removeUnreferencedVersions(
			s, referenced, dryRun, out,
		)

		// Clean up old generations.
		var removedGens int
		if globalDir != "" {
			removedGens += cleanOldGenerations(
				globalDir, storeRoot, dryRun,
			)
		}
		if projGaleDir != "" {
			removedGens += cleanOldGenerations(
				projGaleDir, storeRoot, dryRun,
			)
		}

		// Sweep crash leftovers: transient store entries,
		// stale current-new.* swap symlinks, and ~/.gale/tmp
		// build scratch (gh#78, gh#79).
		sweptArtifacts := sweepCrashLeftovers(
			s, globalDir, projGaleDir, dryRun,
		)

		if removedPkgs == 0 && removedGens == 0 &&
			failedPkgs == 0 && sweptArtifacts == 0 {
			out.Success("Nothing to clean up.")
			return nil
		}

		if dryRun {
			out.Info(fmt.Sprintf(
				"%d version(s), %d generation(s), and "+
					"%d leftover artifact(s) would be removed",
				removedPkgs, removedGens, sweptArtifacts,
			))
			return nil
		}

		out.Success(fmt.Sprintf(
			"Removed %d version(s) and %d generation(s)",
			removedPkgs, removedGens,
		))
		if sweptArtifacts > 0 {
			out.Success(fmt.Sprintf(
				"Swept %d leftover build artifact(s)",
				sweptArtifacts,
			))
		}
		if failedPkgs > 0 {
			return fmt.Errorf("%d package version(s) could not be removed", failedPkgs)
		}
		return nil
	},
}

// collectGCRetention builds gc's full retention set:
//
//   - config pins across ALL hosts — the store is shared by
//     synced configs, so another host's [hosts.*.packages]
//     overlay must keep its store entry alive (gh#48);
//   - everything the active global and project generations
//     link, read from their symlink targets, so gc can never
//     leave current/bin dangling (gh#46);
//   - a best-effort registry runtime-dep top-up (the recipe's
//     current version, when a resolver is available);
//   - the transitive dep closure recorded in each retained
//     package's .gale-deps.toml — the versions installed
//     binaries actually link, which a recipe version bump or
//     an offline resolver miss would otherwise leave
//     unprotected (gh#48).
func collectGCRetention(
	globalDir, projPath, projGaleDir string,
	s *store.Store,
	resolver installer.RecipeResolver,
	out *output.Output,
) map[string]bool {
	referenced := collectReferencedPackagesAllHosts(
		globalDir, projPath, s, out,
	)
	addActiveGenerationRefs(globalDir, s, referenced)
	addActiveGenerationRefs(projGaleDir, s, referenced)
	if resolver != nil {
		expandRuntimeDeps(s, resolver, referenced)
	}
	expandInstalledDeps(s, referenced)
	return referenced
}

// addActiveGenerationRefs adds every name@version the active
// generation under galeDir links to the referenced set. The
// versions come straight from the gen's symlink targets, so
// the keys match store.List output exactly. This protects
// store dirs the live generation still serves — including
// versions a rollback re-activated that no config mentions
// (gh#46). Best-effort: an unreadable generation adds nothing.
func addActiveGenerationRefs(
	galeDir string, s *store.Store, referenced map[string]bool,
) {
	if galeDir == "" {
		return
	}
	pkgs, err := generation.CurrentVersions(galeDir, s.Root)
	if err != nil {
		return
	}
	for name, version := range pkgs {
		referenced[name+"@"+version] = true
	}
}

// expandInstalledDeps adds the transitive dep closure recorded
// in each retained package's .gale-deps.toml (gh#48). The
// registry walk in expandRuntimeDeps retains deps at the
// recipe's CURRENT version, but installed binaries have rpaths
// into the versions recorded at build time — after a recipe
// bump (or offline with a cold cache) only the installed
// metadata knows them. generation.FarmStoreDirs already walks
// that metadata for the dylib farm; reuse it per retained
// package and key the resulting store dirs back into the set.
func expandInstalledDeps(s *store.Store, referenced map[string]bool) {
	keys := make([]string, 0, len(referenced))
	for key := range referenced {
		keys = append(keys, key)
	}
	for _, key := range keys {
		at := lastIndex(key, '@')
		if at < 0 {
			continue
		}
		dirs := generation.FarmStoreDirs(
			map[string]string{key[:at]: key[at+1:]}, s.Root,
		)
		for _, dir := range dirs {
			rel, err := filepath.Rel(s.Root, dir)
			if err != nil || strings.HasPrefix(rel, "..") {
				continue
			}
			parts := strings.SplitN(
				rel, string(filepath.Separator), 3,
			)
			if len(parts) < 2 {
				continue
			}
			referenced[parts[0]+"@"+parts[1]] = true
		}
	}
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
// Returns (removed, failed): the number of versions
// removed (or flagged in dry-run mode) and the number
// of versions that could not be removed due to errors.
func removeUnreferencedVersions(
	s *store.Store,
	referenced map[string]bool,
	dry bool,
	out *output.Output,
) (int, int) {
	installed, err := s.List()
	if err != nil {
		out.Warn(fmt.Sprintf("listing store: %v", err))
		return 0, 0
	}
	var removed, failed int
	for _, pkg := range installed {
		if isReferenced(pkg.Name, pkg.Version, referenced) {
			continue
		}
		if dry {
			out.Info(fmt.Sprintf(
				"Would remove %s@%s",
				pkg.Name, pkg.Version,
			))
			removed++
		} else {
			if err := s.Remove(
				pkg.Name, pkg.Version,
			); err != nil {
				out.Warn(fmt.Sprintf(
					"Failed to remove %s@%s: %v",
					pkg.Name, pkg.Version, err,
				))
				failed++
				continue
			}
			out.Success(fmt.Sprintf(
				"Removed %s@%s",
				pkg.Name, pkg.Version,
			))
			removed++
		}
	}
	return removed, failed
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
		globalDir, projPath, s, nil, out,
	)
}

// collectReferencedPackagesAllHosts is the host-union
// variant: it counts the shared [packages] section plus
// every [hosts.*.packages] overlay in each config, not
// just the current host's flattened view. `gale remove`
// uses it for the cross-scope deletion guard — the store
// is shared across hosts (synced configs), so a pin under
// another host's overlay must keep the store entry alive
// even though ApplyHost would hide it on this machine.
func collectReferencedPackagesAllHosts(
	globalDir, projPath string,
	s *store.Store, out *output.Output,
) map[string]bool {
	referenced := map[string]bool{}
	if globalDir != "" {
		mergeConfigAllHosts(
			filepath.Join(globalDir, "gale.toml"),
			s, referenced, out,
		)
	}
	if projPath != "" {
		mergeConfigAllHosts(projPath, s, referenced, out)
	}
	return referenced
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
			s, referenced, out,
		)
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
	cfg.ApplyHost(config.CurrentHost())
	addPackageRefs(s, cfg.Packages, referenced)
}

// mergeConfigAllHosts is the host-union counterpart of
// mergeConfig: instead of flattening to the current host's
// view, it adds the shared [packages] section plus every
// [hosts.*.packages] overlay. When shared and overlay pin
// different versions of the same package, both versions
// are recorded — the union, not the override.
func mergeConfigAllHosts(
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
	addPackageRefs(s, cfg.Packages, referenced)
	for _, h := range cfg.Hosts {
		addPackageRefs(s, h.Packages, referenced)
	}
}

// addPackageRefs adds each name→version pair to the
// referenced set, resolving through store.StorePath so
// bare versions (jq = "1.8.1") become the canonical
// on-disk form (jq@1.8.1-3) and compare cleanly against
// store.List output. Entries not in the store stay keyed
// on their raw name@version.
func addPackageRefs(
	s *store.Store,
	packages map[string]string,
	referenced map[string]bool,
) {
	for name, version := range packages {
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
// older than the current one. Returns the count of generations
// removed (or flagged in dry-run mode).
//
// The function holds the generation lock for its entire
// execution so it serializes with generation.Build. Inside
// the lock, curGen is read first so that an in-flight Build
// that has created gen/N+1 but not yet swapped current is
// never considered for deletion (n < curGen is the criterion,
// not n != curGen).
func cleanOldGenerations(galeDir, storeRoot string, dry bool) int {
	out := newOutput()
	genRoot := filepath.Join(galeDir, "gen")
	lockPath := filepath.Join(filepath.Dir(storeRoot), "generation.lock")
	var removed int
	_ = filelock.With(lockPath, func() error {
		// Read curGen first (while holding the lock) so the
		// snapshot is consistent with the directory listing.
		curGen, _ := generation.Current(galeDir)
		entries, err := os.ReadDir(genRoot)
		if err != nil {
			return nil //nolint:nilerr
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			n, err := strconv.Atoi(e.Name())
			if err != nil || n >= curGen {
				// Skip non-numeric entries and anything at or
				// above curGen (includes in-flight gen/N+1).
				continue
			}
			genPath := filepath.Join(genRoot, e.Name())
			if dry {
				out.Info(fmt.Sprintf(
					"Would remove generation %d", n,
				))
			} else {
				if err := os.RemoveAll(genPath); err != nil {
					out.Warn(fmt.Sprintf(
						"Failed to remove generation %d: %v",
						n, err,
					))
					continue
				}
				out.Success(fmt.Sprintf(
					"Removed generation %d", n,
				))
			}
			removed++
		}
		return nil
	})
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

// gcSweepGrace is the age guard for crash-leftover sweeps. An
// entry younger than this may belong to an in-flight install
// or build, so it is left for a future gc.
const gcSweepGrace = time.Hour

// gcScratchPrefixes are the ~/.gale/tmp entry name prefixes
// gale's build paths create (see internal/build and
// internal/installer). Only entries matching one of these are
// swept — anything else in tmp is not provably gale-owned.
var gcScratchPrefixes = []string{
	"gale-build-", "gale-install-", "gale-tools-",
	"gale-home-", "gale-tmp-",
}

// sweepCrashLeftovers reclaims artifacts a crashed or killed
// process stranded: transient store entries (.build-*, *.bak,
// *.stream — gh#78), stale current-new.<pid> swap symlinks,
// and ~/.gale/tmp build scratch (gh#79). Everything is guarded
// by gcSweepGrace; the store sweep additionally skips package
// dirs whose lock is concurrently held, and the tmp sweep is
// vetoed entirely while any install is in flight, since
// scratch dirs cannot be attributed to a package. Returns the
// number of entries swept (or flagged in dry-run mode).
//
// Deliberately count-only output: a machine that has crashed
// through many builds can hold thousands of scratch dirs
// (gh#79 reports ~5000), and a per-item line for each would
// drown the rest of the gc report.
func sweepCrashLeftovers(
	s *store.Store, globalDir, projGaleDir string, dry bool,
) int {
	swept := len(s.SweepTransient(gcSweepGrace, dry))
	swept += sweepStaleSwapLinks(globalDir, dry)
	swept += sweepStaleSwapLinks(projGaleDir, dry)
	swept += sweepBuildScratch(s, dry)
	return swept
}

// sweepStaleSwapLinks removes current-new.<pid> symlinks under
// galeDir left behind when a generation swap crashed between
// creating the staging link and renaming it over current
// (gh#78). A live swap completes in milliseconds, so anything
// older than gcSweepGrace is debris.
func sweepStaleSwapLinks(galeDir string, dry bool) int {
	if galeDir == "" {
		return 0
	}
	entries, err := os.ReadDir(galeDir)
	if err != nil {
		return 0
	}
	cutoff := time.Now().Add(-gcSweepGrace)
	var swept int
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "current-new.") {
			continue
		}
		info, err := e.Info() // Lstat: the link's own mtime
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if !dry {
			if err := os.Remove(
				filepath.Join(galeDir, e.Name()),
			); err != nil {
				continue
			}
		}
		swept++
	}
	return swept
}

// sweepBuildScratch removes gale-owned scratch dirs under
// ~/.gale/tmp older than gcSweepGrace (gh#79). Interrupted
// builds skip their cleanup defers, so this is the only
// reclamation path. The whole sweep is skipped while any
// per-package store lock is held: a build in flight may have
// scratch of any age here, and there is no way to map a
// scratch dir back to the package that owns it.
func sweepBuildScratch(s *store.Store, dry bool) int {
	tmpDir := build.TmpDir()
	if tmpDir == "" {
		return 0
	}
	if s.AnyLockHeld() {
		return 0 // an install or build is in flight
	}
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return 0
	}
	cutoff := time.Now().Add(-gcSweepGrace)
	var swept int
	for _, e := range entries {
		if !hasScratchPrefix(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if !dry {
			if err := os.RemoveAll(
				filepath.Join(tmpDir, e.Name()),
			); err != nil {
				continue
			}
		}
		swept++
	}
	return swept
}

// hasScratchPrefix reports whether name matches one of the
// gale-owned scratch dir prefixes.
func hasScratchPrefix(name string) bool {
	for _, prefix := range gcScratchPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
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
		"Resolve recipes from a local directory instead of the registry "+
			"(bare --recipes uses ../gale-recipes/) for runtime-dep retention")
	gcCmd.Flags().Lookup("recipes").NoOptDefVal = "auto"
	rootCmd.AddCommand(gcCmd)
}
