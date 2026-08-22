package main

import (
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

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/filelock"
	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/projects"
	"github.com/kelp/gale/internal/store"
	"github.com/spf13/cobra"
)

var gcCmd = &cobra.Command{
	Use:   "gc",
	Short: "Remove unused package versions and old generations",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runGCMarkSweep(newCmdOutput(cmd))
	},
}

func runGCMarkSweep(out *output.Output) error {
	globalDir, err := galeConfigDir()
	if err != nil {
		return fmt.Errorf("resolving gale home: %w", err)
	}
	storeRoot := defaultStoreRoot()
	s := store.NewStore(storeRoot)

	if !dryRun {
		if err := projects.Prune(globalDir); err != nil {
			out.Warn(fmt.Sprintf("pruning project registry: %v", err))
		}
	}

	unlocks, scopes, err := acquireGCLocks(globalDir, storeRoot)
	if err != nil {
		return err
	}
	defer releaseLocks(unlocks)

	kept, retainedProjects, err := markKeptStoreRels(scopes, storeRoot)
	if err != nil {
		return fmt.Errorf("refusing to sweep the store: %w", err)
	}

	removedPkgs, failedPkgs := sweepUnreferencedStore(s, kept, dryRun, out)
	fetchErr := sweepUnreferencedFetch(s, kept, dryRun, out)

	var removedGens int
	for _, sc := range scopes {
		removedGens += cleanOldGenerationsLocked(sc.GaleDir, dryRun, out)
	}

	sweptArtifacts, err := sweepCrashLeftovers(s, scopes, dryRun)
	if err != nil {
		return err
	}

	return reportGC(out, gcReport{
		removedPkgs: removedPkgs, removedGens: removedGens,
		failedPkgs: failedPkgs, swept: sweptArtifacts,
		retained: retainedProjects, fetchErr: fetchErr,
	})
}

type gcReport struct {
	removedPkgs, removedGens, failedPkgs, swept int
	retained                                    []string
	fetchErr                                    error
}

func reportGC(out *output.Output, r gcReport) error {
	if dryRun && len(r.retained) > 0 {
		out.Info(fmt.Sprintf(
			"Projects contributing retention: %s",
			strings.Join(r.retained, ", "),
		))
	}
	var runErrs []error
	if r.fetchErr != nil {
		runErrs = append(runErrs, r.fetchErr)
	}
	if r.removedPkgs == 0 && r.removedGens == 0 &&
		r.failedPkgs == 0 && r.swept == 0 {
		if err := errors.Join(runErrs...); err != nil {
			return err
		}
		out.Success("Nothing to clean up.")
		return nil
	}
	if dryRun {
		out.Info(fmt.Sprintf(
			"%d version(s), %d generation(s), and "+
				"%d leftover artifact(s) would be removed",
			r.removedPkgs, r.removedGens, r.swept,
		))
		return errors.Join(runErrs...)
	}
	out.Success(fmt.Sprintf(
		"Removed %d version(s) and %d generation(s)",
		r.removedPkgs, r.removedGens,
	))
	if r.swept > 0 {
		out.Success(fmt.Sprintf(
			"Swept %d leftover build artifact(s)", r.swept,
		))
	}
	if r.failedPkgs > 0 {
		runErrs = append(runErrs, fmt.Errorf(
			"%d package version(s) could not be removed", r.failedPkgs,
		))
	}
	return errors.Join(runErrs...)
}

func acquireGCLocks(
	galeHome, storeRoot string,
) ([]func(), []projects.Scope, error) {
	mutateUnlocks, scopes, err := acquireScopeMutateLocks(galeHome)
	if err != nil {
		return nil, nil, err
	}
	genUnlock, err := filelock.Acquire(genLockPath(storeRoot))
	if err != nil {
		releaseLocks(mutateUnlocks)
		return nil, nil, fmt.Errorf("acquire generation.lock: %w", err)
	}
	return append(mutateUnlocks, genUnlock), scopes, nil
}

func acquireScopeMutateLocks(
	galeHome string,
) ([]func(), []projects.Scope, error) {
	for {
		scopes, err := projects.Scopes(galeHome)
		if err != nil {
			return nil, nil, fmt.Errorf("enumerating scopes: %w", err)
		}
		sorted := sortScopesByGaleDir(scopes)
		var unlocks []func()
		var acquireErr error
		for _, sc := range sorted {
			u, err := filelock.Acquire(mutateLockPath(sc.GaleDir))
			if err != nil {
				acquireErr = fmt.Errorf(
					"acquire mutate.lock for %s: %w", sc.Label, err,
				)
				break
			}
			unlocks = append(unlocks, u)
		}
		if acquireErr != nil {
			releaseLocks(unlocks)
			return nil, nil, acquireErr
		}
		again, err := projects.Scopes(galeHome)
		if err != nil {
			releaseLocks(unlocks)
			return nil, nil, fmt.Errorf("enumerating scopes: %w", err)
		}
		if galeDirKey(scopes) == galeDirKey(again) {
			return unlocks, sortScopesByGaleDir(again), nil
		}
		releaseLocks(unlocks)
	}
}

func sortScopesByGaleDir(scopes []projects.Scope) []projects.Scope {
	out := append([]projects.Scope(nil), scopes...)
	sort.Slice(out, func(i, j int) bool {
		return out[i].GaleDir < out[j].GaleDir
	})
	return out
}

func galeDirKey(scopes []projects.Scope) string {
	dirs := make([]string, len(scopes))
	for i, sc := range scopes {
		dirs[i] = sc.GaleDir
	}
	sort.Strings(dirs)
	return strings.Join(dirs, "\n")
}

func releaseLocks(unlocks []func()) {
	for i := len(unlocks) - 1; i >= 0; i-- {
		unlocks[i]()
	}
}

func markKeptStoreRels(
	scopes []projects.Scope, storeRoot string,
) (map[string]bool, []string, error) {
	kept := map[string]bool{}
	var retained []string
	var errs []error
	for _, sc := range scopes {
		dirs, err := generation.KeptStoreDirs(sc.GaleDir, storeRoot)
		if err != nil {
			errs = append(errs, fmt.Errorf(
				"reading kept generations under %s: %w",
				sc.GaleDir, err,
			))
			continue
		}
		for _, dir := range dirs {
			if rel := storeRel(storeRoot, dir); rel != "" {
				kept[rel] = true
			}
		}
		if sc.Label != "the global scope" && len(dirs) > 0 {
			retained = append(retained, filepath.Dir(sc.GaleDir))
		}
	}
	return kept, retained, errors.Join(errs...)
}

func storeRel(storeRoot, dir string) string {
	absStore, err := filepath.EvalSymlinks(storeRoot)
	if err != nil {
		absStore = storeRoot
	}
	absDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		absDir = dir
	}
	if rel := relToStore(absStore, absDir); rel != "" {
		return rel
	}
	return relToStore(storeRoot, dir)
}

func relToStore(root, target string) string {
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return ""
	}
	return rel
}

func sweepUnreferencedStore(
	s *store.Store,
	kept map[string]bool,
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
		if kept[filepath.Join(pkg.Name, pkg.Version)] {
			continue
		}
		if dry {
			out.Info(fmt.Sprintf("Would remove %s@%s", pkg.Name, pkg.Version))
			removed++
			continue
		}
		if err := s.Remove(pkg.Name, pkg.Version); err != nil {
			out.Warn(fmt.Sprintf(
				"Failed to remove %s@%s: %v", pkg.Name, pkg.Version, err,
			))
			failed++
			continue
		}
		out.Success(fmt.Sprintf("Removed %s@%s", pkg.Name, pkg.Version))
		removed++
	}
	return removed, failed
}

func sweepUnreferencedFetch(
	s *store.Store,
	kept map[string]bool,
	dry bool,
	out *output.Output,
) error {
	alive, paths, err := s.FetchStagingAlive()
	if err != nil {
		return fmt.Errorf("listing fetch staging: %w", err)
	}
	if alive {
		return fmt.Errorf(
			"refusing to sweep fetch identities while staging is live: %s",
			paths[0],
		)
	}
	idents, err := s.ListFetch()
	if err != nil {
		return fmt.Errorf("listing fetch identities: %w", err)
	}
	cutoff := time.Now().Add(-gcSweepGrace)
	for _, id := range idents {
		if kept[id.Rel()] {
			continue
		}
		dir := filepath.Join(s.Root, id.Rel())
		info, err := os.Stat(dir)
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if dry {
			out.Info(fmt.Sprintf(
				"Would remove fetch %s@%s-%s", id.Name, id.Version, id.SHA12,
			))
			continue
		}
		if err := s.RemoveFetch(id.Name, id.Version, id.SHA12); err != nil {
			out.Warn(fmt.Sprintf(
				"Failed to remove fetch %s@%s-%s: %v",
				id.Name, id.Version, id.SHA12, err,
			))
			continue
		}
		out.Success(fmt.Sprintf(
			"Removed fetch %s@%s-%s", id.Name, id.Version, id.SHA12,
		))
	}
	return nil
}

// collectKeptRetentionKeys is migrate's view of gc's mark set:
// name@version (or name@version-sha12 for fetch) for every
// kept-generation target across projects.Scopes.
func collectKeptRetentionKeys(
	galeHome, storeRoot string,
) (map[string]bool, error) {
	scopes, err := projects.Scopes(galeHome)
	if err != nil {
		return nil, fmt.Errorf("enumerating scopes: %w", err)
	}
	dirsKept, _, err := markKeptStoreRels(scopes, storeRoot)
	if err != nil {
		return nil, err
	}
	keys := map[string]bool{}
	for rel := range dirsKept {
		parts := strings.Split(rel, string(filepath.Separator))
		switch {
		case len(parts) >= 3 && parts[0] == store.FetchNamespace:
			keys[parts[1]+"@"+parts[2]] = true
		case len(parts) >= 2:
			keys[parts[0]+"@"+parts[1]] = true
		}
	}
	return keys, nil
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
// generation.KeptNumbers does not name. It takes
// generation.lock so unit tests can call it alone.
func cleanOldGenerations(galeDir, storeRoot string, dry bool) int {
	var removed int
	_ = filelock.With(genLockPath(storeRoot), func() error {
		removed = cleanOldGenerationsLocked(galeDir, dry, newOutput())
		return nil
	})
	return removed
}

func cleanOldGenerationsLocked(galeDir string, dry bool, out *output.Output) int {
	if galeDir == "" {
		return 0
	}
	keep, err := generation.KeptNumbers(galeDir)
	if err != nil || len(keep) == 0 {
		return 0
	}
	keepSet := make(map[int]bool, len(keep))
	for _, n := range keep {
		keepSet[n] = true
	}
	genRoot := filepath.Join(galeDir, "gen")
	entries, err := os.ReadDir(genRoot)
	if err != nil {
		return 0
	}
	var removed int
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
			out.Info(fmt.Sprintf("Would remove generation %d", n))
		} else if err := os.RemoveAll(genPath); err != nil {
			out.Warn(fmt.Sprintf("Failed to remove generation %d: %v", n, err))
			continue
		} else {
			out.Success(fmt.Sprintf("Removed generation %d", n))
		}
		removed++
	}
	return removed
}

// gcSweepGrace is the age guard for crash-leftover sweeps. An
// entry younger than this may belong to an in-flight install
// or build, so it is left for a future gc.
const gcSweepGrace = time.Hour

// gcScratchPrefixes are the ~/.gale/tmp entry name prefixes
// gale's scratch paths create (see store.TmpDir and
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
var gcSwapDebrisPrefixes = []string{
	"current-new.", "lib.staging.",
}

// sweepCrashLeftovers reclaims artifacts a crashed or killed
// process stranded: transient store entries (.build-*, *.bak,
// *.stream — gh#78), the PID-scoped staging leftovers of a
// generation swap, and ~/.gale/tmp build scratch (gh#79).
func sweepCrashLeftovers(
	s *store.Store, scopes []projects.Scope, dry bool,
) (int, error) {
	swept := len(s.SweepTransient(gcSweepGrace, dry))
	for _, sc := range scopes {
		swept += sweepStaleSwapDebris(sc.GaleDir, dry)
	}
	scratch, err := sweepBuildScratch(s, dry)
	if err != nil {
		return swept, err
	}
	return swept + scratch, nil
}

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
		info, err := e.Info()
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

func hasSwapDebrisPrefix(name string) bool {
	for _, prefix := range gcSwapDebrisPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func sweepBuildScratch(s *store.Store, dry bool) (int, error) {
	tmpDir, err := store.TmpDir()
	if err != nil {
		return 0, fmt.Errorf("build temp dir: %w", err)
	}
	if s.AnyLockHeld() {
		return 0, nil
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

func hasScratchPrefix(name string) bool {
	for _, prefix := range gcScratchPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func init() {
	rootCmd.AddCommand(gcCmd)
}
