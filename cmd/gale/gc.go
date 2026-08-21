package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/kelp/gale/internal/build"
	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/farm"
	"github.com/kelp/gale/internal/filelock"
	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/projects"
	"github.com/kelp/gale/internal/store"
	"github.com/spf13/cobra"
)

var (
	gcRecipes string
	gcForce   bool
)

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
			if err != nil {
				// gc is destructive: an unresolvable project
				// scope must abort the run, not silently
				// shrink the retained set.
				return fmt.Errorf(
					"resolving project gale dir for %s: %w",
					projPath, err,
				)
			}
			if dir == globalDir {
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
		var pinResolve versionedRecipeResolver
		if ctx, cErr := newCmdContext(gcRecipes, false, false); cErr == nil {
			resolver = ctx.Resolver
			pinResolve = ctx.versionedRecipeResolver()
		}

		// Drop registry entries whose project vanished before
		// computing liveness — vanished means provably absent:
		// gale.toml stats as not-exist (gh#115). Any other
		// stat error keeps the entry; when
		// in doubt, keep. Skipped in dry-run mode: -n must not
		// mutate state.
		if !dryRun && globalDir != "" {
			if err := projects.Prune(globalDir); err != nil {
				out.Warn(fmt.Sprintf(
					"pruning project registry: %v", err,
				))
			}
		}

		// Rebuild generations when the active gen still links a
		// superseded orphan revision, so retention and pruning see
		// recipe-canonical symlinks (gh#137).
		//
		// A failure here is warned about AND collected. It used to be
		// warned about only, which was defensible while the rebuild
		// was a tidy-up whose failure changed nothing a user could
		// observe. Since design revision 6 the rebuild also
		// reconciles the shared library farm and returns when that is
		// left incomplete, and an incomplete farm means binaries
		// cannot load their dylibs. Swallowing it would let gc exit 0
		// having broken exactly what the loud-failure rule exists to
		// surface. gc still finishes its own work first: sweeping and
		// pruning are independent of this.
		//
		// The rebuild takes its versions from the scope's lock when
		// there is a usable one. The recipe is what opened this
		// branch, but under a lock it is not what may be published:
		// relinking the revision the recipe now offers activates a
		// version the lock does not name, and global scope has no
		// activation gate to catch it (gh#197).
		var rebuildErrs []error
		if !dryRun && pinResolve != nil {
			scopes := []struct {
				kind                string
				galeDir, configPath string
			}{
				{"global", globalDir, filepath.Join(globalDir, "gale.toml")},
				{"project", projGaleDir, projPath},
			}
			for _, sc := range scopes {
				if sc.galeDir == "" || sc.configPath == "" {
					continue
				}
				if !generationLinksSupersededOrphan(
					sc.galeDir, storeRoot, sc.configPath, pinResolve,
				) {
					continue
				}
				if err := rebuildUnderLock(genRebuild{
					galeDir:    sc.galeDir,
					storeRoot:  storeRoot,
					configPath: sc.configPath,
					pinResolve: pinResolve,
				}, recoveryRebuild{
					force:         gcForce,
					skipUnchanged: true,
					out:           out,
				}); err != nil {
					out.Warn(fmt.Sprintf(
						"rebuilding %s generation: %v", sc.kind, err,
					))
					rebuildErrs = append(rebuildErrs, fmt.Errorf(
						"rebuilding %s generation: %w", sc.kind, err,
					))
				}
			}
		}

		// Retention covers config pins across ALL hosts,
		// everything the active generation and the branch above
		// it link — in every scope, including every registered
		// project's (gh#115, gh#247) — and each retained
		// package's installed dep closure. Anything retained can
		// never be deleted below, so the active generation needs
		// no rebuild after gc — which also means a `gale
		// generations rollback` survives gc instead of being
		// silently re-advanced to config state (gh#46, gh#47).
		referenced, retainedProjects, retErr := collectGCRetention(
			globalDir, projPath, projGaleDir, s, resolver, pinResolve,
		)
		// An unreadable reference source is not proof of
		// non-reference, so retention is incomplete and every
		// sweep below would delete on a guess (gh#188). Abort in
		// dry-run too: "Would remove jq@1.7" computed from an
		// incomplete set is actively misleading.
		if retErr != nil {
			if !gcForce {
				rebuildErrs = append(rebuildErrs, fmt.Errorf(
					"refusing to sweep the store: %w "+
						"(rerun with --force to sweep anyway)", retErr,
				))
				return errors.Join(rebuildErrs...)
			}
			out.Warn(fmt.Sprintf(
				"sweeping with incomplete retention: %v", retErr,
			))
		}

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
		sweptArtifacts, err := sweepCrashLeftovers(
			s, globalDir, projGaleDir, dryRun,
		)
		if err != nil {
			return err
		}

		// In dry-run mode, make the registry's contribution
		// visible so a stale entry holding store versions
		// alive is diagnosable rather than mysterious (gh#115).
		if dryRun && len(retainedProjects) > 0 {
			out.Info(fmt.Sprintf(
				"Projects contributing retention: %s",
				strings.Join(retainedProjects, ", "),
			))
		}

		// Same reason, for the generations gc never touches: after
		// a rollback the gens above current are skipped by the
		// n >= curGen guard and unreachable to auto-prune's numeric
		// cutoff, so "gc removed nothing" reads as a bug rather
		// than the policy it is (gh#206).
		if dryRun {
			branch := countBranchGens(globalDir, storeRoot) +
				countBranchGens(projGaleDir, storeRoot)
			if branch > 0 {
				out.Info(fmt.Sprintf(
					"Retaining %d %s above current; the next "+
						"rebuild allocates past them, then keep-2 "+
						"prunes history below the new cutoff",
					branch, pluralGeneration(branch),
				))
			}
		}

		if removedPkgs == 0 && removedGens == 0 &&
			failedPkgs == 0 && sweptArtifacts == 0 {
			// A refused or failed rebuild still has to reach the
			// shell. Reporting "nothing to clean up" and exiting 0
			// on a scope gc declined to touch would hide the whole
			// point of collecting the error.
			if err := errors.Join(rebuildErrs...); err != nil {
				return err
			}
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
			rebuildErrs = append(rebuildErrs, fmt.Errorf(
				"%d package version(s) could not be removed", failedPkgs,
			))
		}
		return errors.Join(rebuildErrs...)
	},
}

// collectGCRetention builds gc's full retention set:
//
//   - config pins across ALL hosts — the store is shared by
//     synced configs, so another host's [hosts.*.packages]
//     overlay must keep its store entry alive (gh#48);
//   - everything the active global and project generations link
//     AND everything the generations above them link, read from
//     their symlink targets, so gc can never leave current/bin
//     dangling (gh#46) and never leaves a retained generation
//     without the store dirs it links (gh#247);
//   - the config pins and retained generations of every project
//     in the machine-local registry (<globalDir>/projects), so
//     a gc run from $HOME or project A cannot sweep versions
//     project B still links (gh#115) — every registered project
//     contributes its whole branch, or the rollback-after-gc
//     hole reopens for every scope but the cwd's;
//   - a best-effort registry runtime-dep top-up (the recipe's
//     current version, when a resolver is available);
//   - the transitive dep closure recorded in each retained
//     package's .gale-deps.toml — the versions installed
//     binaries actually link, which a recipe version bump or
//     an offline resolver miss would otherwise leave
//     unprotected (gh#48).
//
// The second return value lists the registered projects that
// contributed retention, for the `gc -n` report.
//
// Every LOCAL reference source is strict: an unreadable config
// or generation yields an error naming the project, and gc's
// caller refuses to sweep on it (gh#188). Errors from all
// projects are joined so one run names every bad one. The
// registry top-up stays best-effort by contrast — see
// expandRuntimeDeps.
func collectGCRetention(
	globalDir, projPath, projGaleDir string,
	s *store.Store,
	resolver installer.RecipeResolver,
	pinResolve versionedRecipeResolver,
) (map[string]bool, []string, error) {
	referenced, err := collectReferencedPackagesAllHosts(
		globalDir, projPath, s, pinResolve,
	)
	errs := []error{
		err,
		addRetainedGenerationRefs(globalDir, s, referenced),
		addRetainedGenerationRefs(projGaleDir, s, referenced),
	}
	retainedProjects, projErr := addRegisteredProjectRefs(
		globalDir, projGaleDir, s, referenced, pinResolve,
	)
	errs = append(errs, projErr)
	if resolver != nil {
		expandRuntimeDeps(s, resolver, referenced)
	}
	errs = append(errs, expandInstalledDeps(s, referenced))
	return referenced, retainedProjects, errors.Join(errs...)
}

// addRegisteredProjectRefs unions in the retention of every
// project in the machine-local registry (gh#115): each live
// registered project contributes its gale.toml pins (all
// hosts) and everything its active generation links.
// Returns the project paths that contributed. Vanished
// projects (gale.toml provably absent — see projects.Lives)
// contribute nothing — Prune removes them on non-dry runs.
// Projects already covered by the direct global/cwd-project
// passes are skipped.
//
// A project whose liveness stat fails any other way counts as
// live, and reading its references is then strict: an
// unreadable registry, config or generation returns an error
// naming the project instead of quietly contributing nothing
// (gh#188). There is no conservative middle ground — gc cannot
// enumerate the keys an unreadable project would keep, so
// "retain what it might reference" means "retain everything",
// which is what refusing to sweep already achieves, loudly.
func addRegisteredProjectRefs(
	globalDir, projGaleDir string,
	s *store.Store,
	referenced map[string]bool,
	pinResolve versionedRecipeResolver,
) ([]string, error) {
	if globalDir == "" {
		return nil, nil
	}
	regProjects, err := projects.List(globalDir)
	if err != nil {
		return nil, fmt.Errorf("reading project registry: %w", err)
	}
	var retained []string
	var errs []error
	for _, proj := range regProjects {
		cfgPath := filepath.Join(proj, "gale.toml")
		galeDir, err := galeDirForConfig(cfgPath)
		if err != nil ||
			sameDir(galeDir, globalDir) ||
			(projGaleDir != "" && sameDir(galeDir, projGaleDir)) {
			continue // unresolvable or already covered
		}
		if !projects.Lives(proj) {
			continue // provably absent; Prune cleans it up
		}
		if err := addProjectPinRefs(
			cfgPath, s, referenced, pinResolve,
		); err != nil {
			errs = append(errs, projectRefError(proj, err))
			continue
		}
		if err := addRetainedGenerationRefs(
			galeDir, s, referenced,
		); err != nil {
			errs = append(errs, projectRefError(proj, err))
			continue
		}
		retained = append(retained, proj)
	}
	return retained, errors.Join(errs...)
}

// projectRefError names the project a retention failure came
// from, so a refused sweep tells the user which mount to fix.
func projectRefError(proj string, err error) error {
	return fmt.Errorf(
		"reading references for project %s: %w", proj, err,
	)
}

// addProjectPinRefs adds the gale.toml pins of the project
// owning cfgPath. A version the active generation doesn't
// link (pin edited, sync not yet run) survives only through
// its pin.
//
// The stat splits missing from unreadable. Missing gale.toml
// contributes no pins (the project is vanished or not yet
// initialized). Any other stat error is an unreadable
// gale.toml and refuses the sweep.
func addProjectPinRefs(
	cfgPath string,
	s *store.Store,
	referenced map[string]bool,
	pinResolve versionedRecipeResolver,
) error {
	_, err := os.Stat(cfgPath)
	switch {
	case err == nil:
		return mergeConfigAllHosts(
			cfgPath, s, referenced, pinResolve,
		)
	case errors.Is(err, fs.ErrNotExist):
		return nil
	default:
		// *fs.PathError: already names the op and the path.
		return err
	}
}

// addRetainedGenerationRefs adds every name@version the RETAINED
// generations under galeDir link to the referenced set. The
// versions come straight from the gens' symlink targets, so the
// keys match store.List output exactly. This protects store dirs
// a live generation still serves — including versions a rollback
// re-activated that no config mentions (gh#46).
//
// Retained, not active. cleanOldGenerations already keeps every
// generation directory at or above current — the branch a
// rollback abandoned, retained so a roll-forward can return to
// it (gh#189). Nothing kept the store versions those generations
// link, so roll back, gc, roll forward activated dangling PATH
// entries with no error at any step (gh#247). The retained set
// is the exact complement of what cleanOldGenerations removes.
//
// An unresolvable current pointer is an error, not an empty
// contribution: everything those generations link would otherwise
// become invisible to retention and be swept (gh#188). A gale dir
// with no current symlink is not an error — generation.Current
// reports gen 0 for it, and every generation is then retained.
//
// The read is strict all the way down (gh#210): a generation
// directory the walk cannot read answers "links nothing" through
// the lenient reader, which is the same fail-open one layer
// lower. generation.List is backed by that lenient reader and
// must never be substituted here.
func addRetainedGenerationRefs(
	galeDir string, s *store.Store, referenced map[string]bool,
) error {
	if galeDir == "" {
		return nil
	}
	pkgs, err := generation.RetainedVersionsStrict(galeDir, s.Root)
	if err != nil {
		return fmt.Errorf(
			"reading retained generations under %s: %w", galeDir, err,
		)
	}
	for name, versions := range pkgs {
		for _, version := range versions {
			referenced[name+"@"+version] = true
		}
	}
	return nil
}

// expandInstalledDeps adds the transitive dep closure recorded
// in each retained package's .gale-deps.toml (gh#48). The
// registry walk in expandRuntimeDeps retains deps at the
// recipe's CURRENT version, but installed binaries have rpaths
// into the versions recorded at build time — after a recipe
// bump (or offline with a cold cache) only the installed
// metadata knows them. generation.FarmStoreDirsStrict already
// walks that metadata for the dylib farm; reuse it per retained
// package and key the resulting store dirs back into the set.
//
// Strict, not the lenient FarmStoreDirs the farm rebuild uses
// (gh#210): an unreadable .gale-deps.toml drops its whole subtree
// from the closure, and a subtree missing from retention is a
// subtree the sweep deletes. Keys are walked in sorted order so a
// run that hits several unreadable files names them in the same
// order every time; map order would shuffle the message.
func expandInstalledDeps(s *store.Store, referenced map[string]bool) error {
	keys := make([]string, 0, len(referenced))
	for key := range referenced {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var errs []error
	for _, key := range keys {
		at := strings.LastIndexByte(key, '@')
		if at < 0 {
			continue
		}
		dirs, err := generation.FarmStoreDirsStrict(
			map[string]string{key[:at]: key[at+1:]}, s.Root,
		)
		if err != nil {
			errs = append(errs, err)
			continue
		}
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
	return errors.Join(errs...)
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

// collectReferencedPackagesAllHosts is the host-union
// variant: it counts the shared [packages] section plus
// every [hosts.*.packages] overlay in each config, not
// just the current host's flattened view. `gale remove`
// uses it for the cross-scope deletion guard — the store
// is shared across hosts (synced configs), so a pin under
// another host's overlay must keep the store entry alive
// even though ApplyHost would hide it on this machine.
//
// Returns the partial set alongside any read error so callers
// can decide: gc refuses to sweep, `gale remove` refuses to
// delete (gh#188).
func collectReferencedPackagesAllHosts(
	globalDir, projPath string,
	s *store.Store,
	pinResolve versionedRecipeResolver,
) (map[string]bool, error) {
	referenced := map[string]bool{}
	var errs []error
	if globalDir != "" {
		errs = append(errs, mergeConfigAllHosts(
			filepath.Join(globalDir, "gale.toml"),
			s, referenced, pinResolve,
		))
	}
	if projPath != "" {
		errs = append(errs, mergeConfigAllHosts(
			projPath, s, referenced, pinResolve,
		))
	}
	return referenced, errors.Join(errs...)
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
	pinResolve versionedRecipeResolver,
) (map[string]bool, error) {
	referenced := map[string]bool{}
	var errs []error
	if globalDir != "" {
		errs = append(errs, mergeConfig(
			filepath.Join(globalDir, "gale.toml"),
			s, referenced, pinResolve,
		))
	}
	if projPath != "" {
		errs = append(errs, mergeConfig(
			projPath, s, referenced, pinResolve,
		))
	}
	if resolver != nil {
		expandRuntimeDeps(s, resolver, referenced)
	}
	return referenced, errors.Join(errs...)
}

// mergeConfig reads a gale.toml and adds its packages
// to the referenced set. A missing config is fine and returns
// nil; every other failure is returned, because a config that
// exists but cannot be read or parsed hides pins rather than
// proving there are none (gh#188). Each entry is resolved via
// store.StorePath so the referenced key always matches the
// on-disk version name produced by store.List.
func mergeConfig(
	path string,
	s *store.Store,
	referenced map[string]bool,
	pinResolve versionedRecipeResolver,
) error {
	data, err := readReferenceSource(path)
	if err != nil {
		return err
	}
	if data == nil {
		return nil // missing config is fine
	}
	cfg, err := config.ParseGaleConfig(string(data))
	if err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}
	// Host-scoped pins are packages this machine's generation links.
	// Applying no overlay would drop every one of them from the
	// retention set, and gc deletes what retention does not name —
	// the cross-scope deletion class ad4e685 and 289d13b came from
	// (gh#254).
	host, err := config.CurrentHost()
	if err != nil {
		return err
	}
	cfg.ApplyHost(host)
	addPackageRefs(s, cfg.Packages, referenced, pinResolve)
	return nil
}

// readReferenceSource reads a file gc derives retention from.
// It returns (nil, nil) when the file is absent — a pin source
// that was never there references nothing — and an error for
// every other failure, which hides pins instead of disproving
// them (gh#188). The error is returned unwrapped: os.ReadFile
// yields a *fs.PathError that already names the operation and
// the path, and callers add the project.
func readReferenceSource(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}

// mergeConfigAllHosts is the host-union counterpart of
// mergeConfig: instead of flattening to the current host's
// view, it adds the shared [packages] section plus every
// [hosts.*.packages] overlay. When shared and overlay pin
// different versions of the same package, both versions
// are recorded — the union, not the override. Missing,
// unreadable and unparsable split as in mergeConfig.
func mergeConfigAllHosts(
	path string,
	s *store.Store,
	referenced map[string]bool,
	pinResolve versionedRecipeResolver,
) error {
	data, err := readReferenceSource(path)
	if err != nil {
		return err
	}
	if data == nil {
		return nil // missing config is fine
	}
	cfg, err := config.ParseGaleConfig(string(data))
	if err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}
	addPackageRefs(s, cfg.Packages, referenced, pinResolve)
	addAllHostPackageRefs(string(data), s, referenced, pinResolve)
	return nil
}

// addAllHostPackageRefs adds every `packages` table found at
// any depth under [hosts] to the referenced set, by walking
// the raw TOML structure instead of the typed config.
//
// The typed GaleConfig.Hosts map silently drops an overlay
// whose host key contains an unquoted dot: a hostname like
// "Mac-123.local" splits into nested tables
// (hosts."Mac-123"."local".packages), which decode into no
// HostConfig field and vanish. The pin is still plainly there
// in the file, and gc is destructive — a pin retention cannot
// see is a store entry it deletes. So the host-union walk must
// be structural: anything that looks like a packages table
// under [hosts] keeps its store entries alive.
func addAllHostPackageRefs(
	data string, s *store.Store, referenced map[string]bool,
	pinResolve versionedRecipeResolver,
) {
	var raw map[string]any
	if _, err := toml.Decode(data, &raw); err != nil {
		return // mergeConfigAllHosts already rejected it
	}
	hosts, ok := raw["hosts"].(map[string]any)
	if !ok {
		return
	}
	for _, node := range hosts {
		walkHostPackages(node, s, referenced, pinResolve)
	}
}

// walkHostPackages recursively visits a table under [hosts]
// and adds every nested `packages` table's name→version pairs
// to the referenced set. Non-table nodes and non-string
// versions are ignored.
func walkHostPackages(
	node any, s *store.Store, referenced map[string]bool,
	pinResolve versionedRecipeResolver,
) {
	table, ok := node.(map[string]any)
	if !ok {
		return
	}
	for key, v := range table {
		if key == "packages" {
			pkgs, ok := v.(map[string]any)
			if !ok {
				continue
			}
			m := make(map[string]string, len(pkgs))
			for name, ver := range pkgs {
				if vs, ok := ver.(string); ok {
					m[name] = vs
				}
			}
			addPackageRefs(s, m, referenced, pinResolve)
			continue
		}
		walkHostPackages(v, s, referenced, pinResolve)
	}
}

// addPackageRefs adds each name→version pair to the
// referenced set, resolving through storeRetentionKey so
// bare versions (jq = "1.8.1") become the recipe's
// canonical on-disk form (jq@1.8.1-3) when a resolver is
// available, instead of the highest revision on disk.
// Entries not in the store stay keyed on their raw
// name@version.
func addPackageRefs(
	s *store.Store,
	packages map[string]string,
	referenced map[string]bool,
	pinResolve versionedRecipeResolver,
) {
	for name, version := range packages {
		referenced[storeRetentionKey(s, name, version, pinResolve)] = true
	}
}

// cleanOldGenerations removes generation directories that
// generation.RetainedNumbers does not name — the keep-2 window
// plus the branch above current. Returns the count of
// generations removed (or flagged in dry-run mode).
//
// The function holds the generation lock for its entire
// execution so it serializes with generation.Build. Inside
// the lock, RetainedNumbers is the sole policy so an in-flight
// Build that has created gen/N+1 but not yet swapped current
// is retained (it is at or above the cutoff), and a directory
// gc keeps cannot lose its store closure (gh#247).
func cleanOldGenerations(galeDir, storeRoot string, dry bool) int {
	out := newOutput()
	genRoot := filepath.Join(galeDir, "gen")
	lockPath := genLockPath(storeRoot)
	var removed int
	_ = filelock.With(lockPath, func() error {
		keep, err := generation.RetainedNumbers(galeDir)
		if err != nil {
			return nil //nolint:nilerr
		}
		keepSet := make(map[int]bool, len(keep))
		for _, n := range keep {
			keepSet[n] = true
		}
		entries, err := os.ReadDir(genRoot)
		if err != nil {
			return nil //nolint:nilerr
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			n, err := strconv.Atoi(e.Name())
			if err != nil || keepSet[n] {
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

// countBranchGens returns how many generations sit above current
// in the given gale dir — the branch a rollback abandoned, which
// gc retains deliberately. An empty dir or an unreadable listing
// counts zero: this only feeds a dry-run notice, and gc must not
// fail on it.
func countBranchGens(galeDir, storeRoot string) int {
	if galeDir == "" {
		return 0
	}
	gens, err := generation.List(galeDir, storeRoot)
	if err != nil {
		return 0
	}
	cur := currentGenNumber(gens)
	if cur == 0 {
		return 0
	}
	var n int
	for _, g := range gens {
		if g.Number > cur {
			n++
		}
	}
	return n
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
//
// Deliberately best-effort where the local reference sources
// are strict (gh#188). This is a TOP-UP over expandInstalledDeps,
// which reads each retained package's on-disk .gale-deps.toml —
// the deps installed binaries actually link. Aborting whenever
// the registry is unreachable would make gc fail under
// GALE_OFFLINE=1, a supported mode, in exchange for retention
// the installed metadata already covers.
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
		at := strings.LastIndexByte(key, '@')
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

		r, err := resolver(context.Background(), name)
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
			depRecipe, rErr := resolver(context.Background(), dep)
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
	"gale-home-", "gale-tmp-", "gale-git-",
}

// gcSwapDebrisPrefixes are the ~/.gale entry name prefixes a
// PID-scoped, rename-published mutation leaves behind when the
// process dies mid-operation: the generation swap's staging symlink
// (gh#78) and the farm image staged beside ~/.gale/lib (gh#184).
// Both are inert — nothing resolves through either — so they are
// swept on age alone, like every other crash leftover.
var gcSwapDebrisPrefixes = []string{
	"current-new.", farm.StagingPrefix,
}

// sweepCrashLeftovers reclaims artifacts a crashed or killed
// process stranded: transient store entries (.build-*, *.bak,
// *.stream — gh#78), the PID-scoped staging leftovers of a
// generation swap or a farm rebuild, and ~/.gale/tmp build
// scratch (gh#79). Everything is guarded
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
) (int, error) {
	swept := len(s.SweepTransient(gcSweepGrace, dry))
	swept += sweepStaleSwapDebris(globalDir, dry)
	swept += sweepStaleSwapDebris(projGaleDir, dry)
	scratch, err := sweepBuildScratch(s, dry)
	if err != nil {
		return swept, err
	}
	return swept + scratch, nil
}

// sweepStaleSwapDebris removes the PID-scoped leftovers under
// galeDir of a mutation that died between staging its new state and
// renaming it into place: a generation swap's current-new.<pid>
// symlink (gh#78), a farm rebuild's lib.staging.<pid> image
// (gh#184). A live swap completes in milliseconds, so anything
// older than gcSweepGrace is debris.
//
// RemoveAll, because the farm image is a directory of symlinks
// while the swap leftover is a single link.
func sweepStaleSwapDebris(galeDir string, dry bool) int {
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
		if !hasSwapDebrisPrefix(e.Name()) {
			continue
		}
		info, err := e.Info() // Lstat: the entry's own mtime
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if !dry {
			if err := os.RemoveAll(
				filepath.Join(galeDir, e.Name()),
			); err != nil {
				continue
			}
		}
		swept++
	}
	return swept
}

// hasSwapDebrisPrefix reports whether name matches one of the
// PID-scoped staging prefixes gale publishes state through.
func hasSwapDebrisPrefix(name string) bool {
	for _, prefix := range gcSwapDebrisPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// sweepBuildScratch removes gale-owned scratch dirs under the
// build scratch dir older than gcSweepGrace (gh#79). Interrupted
// builds skip their cleanup defers, so this is the only
// reclamation path. The whole sweep is skipped while any
// per-package store lock is held: a build in flight may have
// scratch of any age here, and there is no way to map a
// scratch dir back to the package that owns it.
//
// The build.TmpDir error is returned rather than absorbed into a
// zero count. gc's job is to report what it reclaimed, and "0
// swept" for an unreachable scratch dir is indistinguishable
// from "0 swept because there was nothing there" — the same
// silent-degradation shape gh#235 removed from TmpDir itself.
func sweepBuildScratch(s *store.Store, dry bool) (int, error) {
	tmpDir, err := build.TmpDir()
	if err != nil {
		return 0, fmt.Errorf("build temp dir: %w", err)
	}
	if s.AnyLockHeld() {
		return 0, nil // an install or build is in flight
	}
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", tmpDir, err)
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
	return swept, nil
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

func init() {
	gcCmd.Flags().StringVar(&gcRecipes, "recipes", "",
		"Resolve recipes from a local directory instead of the registry "+
			"for runtime-dep retention")
	gcCmd.Flags().BoolVar(&gcForce, "force", false,
		"Sweep even when a project's config or generation cannot be read")
	rootCmd.AddCommand(gcCmd)
}
