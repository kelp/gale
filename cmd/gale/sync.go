package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/kelp/gale/internal/build"
	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/parallel"
	"github.com/spf13/cobra"
)

var (
	syncRecipes string
	syncBuild   bool
	syncGlobal  bool
	syncProject bool
)

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Install all packages in gale.toml",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if syncGlobal && syncProject {
			return fmt.Errorf(
				"cannot use both --global and --project",
			)
		}
		return runSync(syncRecipes, syncBuild, syncGlobal,
			syncProject, "")
	},
}

// runSync performs the sync operation: resolves recipes,
// installs missing packages, and rebuilds the generation.
// When projectDir is non-empty, sync targets that specific
// project directory regardless of cwd or scope flags.
func runSync(recipesPath string, buildOnly, global, project bool, projectDir string) error {
	out := newOutput()

	ctx, err := newCmdContext(recipesPath, false, false)
	if err != nil {
		return err
	}

	// Explicit project directory takes precedence over
	// scope flags. Used by syncIfNeeded when shell/run
	// are invoked with --project.
	if projectDir != "" {
		ctx.GalePath = filepath.Join(projectDir, "gale.toml")
		ctx.GaleDir = filepath.Join(projectDir, ".gale")
	} else if global || project {
		// Validate that --project requires an existing project.
		if project {
			cwd, cwdErr := os.Getwd()
			if cwdErr != nil {
				return fmt.Errorf("getting working dir: %w", cwdErr)
			}
			if _, pErr := projectConfigPath(cwd); pErr != nil {
				return fmt.Errorf("no project found — run 'gale init' first")
			}
		}
		// Override scope when -g or -p is set.
		galePath, pathErr := resolveConfigPath(global)
		if pathErr != nil {
			return pathErr
		}
		galeDir, dirErr := galeDirForConfig(galePath)
		if dirErr != nil {
			return dirErr
		}
		ctx.GalePath = galePath
		ctx.GaleDir = galeDir
	}

	// newCmdContext registered the auto-detected scope; the
	// overrides above may have re-pointed the context at a
	// different project (shell/run's projectDir path), so
	// register the effective one too (gh#115). Dedup makes
	// the repeat a no-op.
	registerProject(ctx.GalePath)

	if buildOnly {
		ctx.Installer.SourceOnly = true
	}

	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}

	if len(cfg.Packages) == 0 {
		out.Info("No packages to sync.")
		return nil
	}

	// Read lockfile for SHA256 verification.
	lp, err := lockfilePath(ctx.GalePath)
	if err != nil {
		return err
	}
	lf, err := lockfile.Read(lp)
	if err != nil {
		return fmt.Errorf("reading lockfile: %w", err)
	}

	items := sortedSyncItems(cfg.Packages)
	// Per-package work is HTTP-bound (recipe fetch + binary
	// download). The same resolved parallelism bounds the
	// Installer's Downloads limiter, so package-level fan-out and
	// per-package dep downloads share one configured ceiling.
	syncWorkers := ctx.Parallelism
	// Errors slice is always nil — runSyncOne captures all errors in
	// syncOutcome fields, never returns one.
	outcomes, _ := parallel.Map(context.Background(), items, syncWorkers,
		func(_ context.Context, w syncItem) (syncOutcome, error) {
			return runSyncOne(ctx, lf, w, dryRun), nil
		})

	var installed, failed int
	for _, o := range outcomes {
		name := o.name
		version := o.version
		switch {
		case o.upToDate:
			if dryRun {
				out.Info(fmt.Sprintf(
					"skip %s@%s (up to date)",
					name, version,
				))
			} else {
				out.Info(fmt.Sprintf(
					"%s@%s up to date", name, version,
				))
			}
		case dryRun:
			out.Info(fmt.Sprintf(
				"install %s@%s (stale)", name, version,
			))
			installed++
		case o.resolveErr != nil:
			out.Warn(fmt.Sprintf(
				"%s@%s: %v. "+
					"Run 'gale update %s' to install latest.",
				name, version, o.resolveErr, name,
			))
			failed++
		case o.installErr != nil:
			if errors.Is(o.installErr, build.ErrUnsupportedPlatform) {
				out.Warn(fmt.Sprintf(
					"%s does not support %s/%s",
					name, runtime.GOOS, runtime.GOARCH,
				))
			} else {
				out.Warn(fmt.Sprintf(
					"Failed to install %s: %v", name, o.installErr,
				))
			}
			failed++
		default:
			if o.stale {
				out.Info(fmt.Sprintf(
					"%s@%s stale — deps changed; reinstalling",
					name, version,
				))
			}
			out.Info(fmt.Sprintf("Installing %s@%s...",
				name, version))
			if o.shaChanged {
				out.Warn(fmt.Sprintf(
					"%s@%s SHA256 changed since last sync "+
						"(lock: %s..., got: %s...) — "+
						"updating lockfile",
					name, version,
					shortSHA(o.priorSHA),
					shortSHA(o.result.SHA256),
				))
			}
			reportResult(out, o.result, "Installed", "built from source")
			if o.lockfileErr != nil {
				out.Warn(fmt.Sprintf(
					"updating lockfile for %s: %v", name, o.lockfileErr,
				))
			}
			installed++
		}
	}

	configChanged := generationDrifted(
		ctx.GaleDir, ctx.StoreRoot, cfg.Packages,
		ctx.versionedRecipeResolver(),
	)

	if err := finishSync(dryRun, failed, installed, configChanged, ctx.RebuildGenerationLenient); err != nil {
		if failed > 0 {
			out.Warn(fmt.Sprintf(
				"Sync finished with %d error(s)", failed,
			))
			return err
		}
		return fmt.Errorf("rebuild generation: %w", err)
	}

	out.Success(fmt.Sprintf(
		"Sync complete: %d installed, %d up to date",
		installed,
		len(cfg.Packages)-installed,
	))
	return nil
}

// generationDrifted reports whether the active generation's
// package set differs from the config. Used by sync to decide
// whether to rebuild when no installs were performed —
// rebuilding regardless would bump the generation counter on
// every no-op sync; skipping when packages have been removed
// would leave the dropped package's symlink in current/bin.
func generationDrifted(
	galeDir, storeRoot string,
	want map[string]string,
	pinResolve versionedRecipeResolver,
) bool {
	active, err := generation.CurrentVersions(galeDir, storeRoot)
	if err != nil {
		// On read error, prefer to rebuild — better a wasted
		// gen than a stale PATH. The rebuild itself will
		// surface a follow-on error if the state is broken.
		return true
	}
	if len(active) != len(want) {
		return true
	}
	// Config versions are bare by convention ("1.8.1") while the
	// active generation's symlinks carry the canonical store-dir
	// basename ("1.8.1-4"); comparing them raw reported drift on
	// every run, so each no-op sync rebuilt and re-swapped a new
	// generation (gh#49). Resolve the config pins to the store-dir
	// basenames a fresh build would link before comparing.
	expected := generation.ActiveVersions(
		canonicalizeForBuild(want, pinResolve), storeRoot,
	)
	for name, version := range expected {
		if active[name] != version {
			return true
		}
	}
	return false
}

// finishSync rebuilds the generation so packages that did
// install land on PATH, then surfaces any install failure.
// Issue #20: a single broken recipe used to leave the user
// with no current symlink at all. Rebuilding first keeps
// partial progress usable; the failure error still
// propagates so the exit code is non-zero.
//
// configChanged signals that the active generation no longer
// matches the config (e.g., a package was removed from
// gale.toml). The rebuild must run even when nothing was
// installed; otherwise the removed package's symlink stays
// active in current/bin.
func finishSync(dryRun bool, failed int, installed int, configChanged bool, rebuild func() error) error {
	if dryRun {
		return nil
	}
	if installed == 0 && failed == 0 && !configChanged {
		return nil // nothing changed — skip rebuild
	}
	rebuildErr := rebuild()
	if failed > 0 {
		if rebuildErr != nil {
			return fmt.Errorf("%d package(s) could not be synced; rebuild: %w",
				failed, rebuildErr)
		}
		return fmt.Errorf("%d package(s) could not be synced", failed)
	}
	return rebuildErr
}

// syncItem is the per-package unit of work passed to runSyncOne.
type syncItem struct {
	name, version string
}

// syncOutcome is the result of one runSyncOne call. It is
// pure data — the caller emits all user-visible output after
// the parallel worker barrier so lines are printed in a
// deterministic order.
type syncOutcome struct {
	name, version string
	upToDate      bool
	stale         bool
	resolveErr    error
	installErr    error
	result        *installer.InstallResult
	shaChanged    bool
	priorSHA      string
	lockfileErr   error
}

// sortedSyncItems converts cfg.Packages to a syncItem slice
// ordered by name. Used by runSync so per-package output is
// emitted in a stable order across runs regardless of which
// worker finished first.
func sortedSyncItems(pkgs map[string]string) []syncItem {
	names := make([]string, 0, len(pkgs))
	for name := range pkgs {
		names = append(names, name)
	}
	sort.Strings(names)
	items := make([]syncItem, len(names))
	for i, name := range names {
		items[i] = syncItem{name: name, version: pkgs[name]}
	}
	return items
}

// runSyncOne is the per-package body of sync, extracted so the
// outer loop can dispatch it under a parallel worker pool.
// Pure with respect to output (no fmt.Println / out.Info calls);
// caller emits user-visible lines after the worker barrier.
// installedStale reports whether an already-installed package must be
// reinstalled. It evaluates staleness against the store dir a
// reinstall writes — the recipe's canonical version-revision — not the
// bare pin's highest on-disk revision. An orphan dir whose revision
// exceeds the recipe's (e.g. left by a withdrawn recipe revision)
// would otherwise shadow the rebuild target: the check reads the stale
// orphan while Reinstall writes the recipe revision, so every sync
// rebuilds forever (the direnv-stall loop).
//
// When the recipe cannot be resolved (e.g. offline) it falls back to
// the bare resolution and reports stale only for pre-revision installs
// missing deps metadata, so installed packages still report up to date
// rather than churn.
func installedStale(ctx *cmdContext, w syncItem) bool {
	r, err := ctx.ResolveVersionedRecipe(w.name, w.version)
	if err != nil {
		storeDir, ok := ctx.Installer.Store.StorePath(w.name, w.version)
		return ok && !installer.HasDepsMetadata(storeDir)
	}

	// Prefer the recipe's canonical dir; fall back to the bare
	// resolution when that revision is not yet on disk.
	storeDir, ok := ctx.Installer.Store.StorePath(w.name, r.Package.Full())
	if !ok {
		storeDir, ok = ctx.Installer.Store.StorePath(w.name, w.version)
	}
	if !ok {
		return false
	}
	if !installer.HasDepsMetadata(storeDir) {
		// Pre-revision install — soft migration: mark stale.
		return true
	}
	stale, staleErr := installer.IsStale(storeDir, r, ctx.Resolver)
	return staleErr == nil && stale
}

func runSyncOne(ctx *cmdContext, lf *lockfile.LockFile, w syncItem, dryRun bool) syncOutcome {
	outcome := syncOutcome{name: w.name, version: w.version}

	// Step a/b: check if already installed and whether stale.
	if ctx.Installer.Store.IsInstalled(w.name, w.version) {
		outcome.stale = installedStale(ctx, w)

		// Step c: not stale — up to date, no install.
		if !outcome.stale {
			outcome.upToDate = true
			return outcome
		}
	}

	// Step d: dry-run with something to do — return without installing.
	if dryRun {
		return outcome
	}

	// Step e: resolve recipe.
	r, err := ctx.ResolveVersionedRecipe(w.name, w.version)
	if err != nil {
		outcome.resolveErr = err
		return outcome
	}

	// Step f: install or reinstall.
	var result *installer.InstallResult
	if outcome.stale {
		result, err = ctx.Installer.Reinstall(r)
	} else {
		result, err = ctx.Installer.Install(r)
	}
	if err != nil {
		outcome.installErr = err
		return outcome
	}
	outcome.result = result

	// Step g: compare lockfile SHA.
	if locked, ok := lf.Packages[w.name]; ok &&
		locked.SHA256 != "" &&
		result.SHA256 != "" &&
		locked.SHA256 != result.SHA256 {
		outcome.shaChanged = true
		outcome.priorSHA = locked.SHA256
	}

	// Step h: write lockfile (non-fatal).
	if result.SHA256 != "" {
		lp, lpErr := lockfilePath(ctx.GalePath)
		if lpErr != nil {
			outcome.lockfileErr = lpErr
		} else if wErr := updateLockfile(lp, w.name, r.Package.Full(), result.SHA256, result.ManifestDigest); wErr != nil {
			outcome.lockfileErr = wErr
		}
	}

	return outcome
}

// shortSHA returns the first 12 characters of a SHA256 hex string for
// display. If s is shorter than 12 (e.g. due to truncation or a bug),
// it returns s unchanged rather than panicking.
func shortSHA(s string) string {
	if len(s) < 12 {
		return s
	}
	return s[:12]
}

func init() {
	syncCmd.Flags().BoolVarP(&syncGlobal, "global", "g",
		false, "Sync global packages")
	syncCmd.Flags().BoolVarP(&syncProject, "project", "p",
		false, "Sync project packages")
	syncCmd.Flags().StringVar(&syncRecipes, "recipes", "",
		"Resolve recipes from a local directory instead of the registry")
	syncCmd.Flags().BoolVar(&syncBuild, "build", false,
		"Build all packages from source (skip prebuilt binaries)")
	rootCmd.AddCommand(syncCmd)
}
