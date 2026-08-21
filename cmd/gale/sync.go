package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/kelp/gale/internal/build"
	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/depsmeta"
	"github.com/kelp/gale/internal/farm"
	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/lockplan"
	"github.com/kelp/gale/internal/output"
	"github.com/spf13/cobra"
)

var (
	syncRecipes      string
	syncBuild        bool
	syncGlobal       bool
	syncProject      bool
	syncNoFrozen     bool
	syncOnlyIfNeeded bool
)

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Install all packages in gale.toml",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateScopeFlags(syncGlobal, syncProject); err != nil {
			return err
		}
		return runSync(syncRun{
			RecipesPath: syncRecipes,
			BuildOnly:   syncBuild,
			Global:      syncGlobal,
			Project:     syncProject,
			IfNeeded:    syncOnlyIfNeeded,
		})
	},
}

// syncRun is runSync's inputs. runSync already sat at the
// argument limit; IfNeeded cannot be a sixth bool without
// revive, and mutating the cobra syncOnlyIfNeeded global
// from shell/run would leak the flag across commands.
type syncRun struct {
	RecipesPath string
	BuildOnly   bool
	Global      bool
	Project     bool
	ProjectDir  string
	IfNeeded    bool
}

// runSync performs the sync operation: resolves recipes,
// installs missing packages, and rebuilds the generation.
// When ProjectDir is non-empty, sync targets that specific
// project directory regardless of cwd or scope flags.
//
// Every exit records a completion stamp (gh#186). The result is a
// named one so the deferred writer records the command's own verdict
// rather than a separately maintained belief about it.
func runSync(s syncRun) (err error) {
	out := newOutput()

	cc, err := newCmdContext(s.RecipesPath, s.Global, s.Project)
	if err != nil {
		return err
	}

	retargetSync(cc, s.ProjectDir)

	host, err := config.CurrentHost()
	if err != nil {
		return err
	}

	ctx := context.Background()
	if s.IfNeeded {
		var cancel context.CancelFunc
		ctx, cancel = ifNeededContext()
		defer cancel()
	}

	record, withheld := beginSyncStamp(out, cc.GaleDir, cc.GalePath, host, s.IfNeeded)
	if withheld {
		return nil
	}
	// Declared here, before the recorder, so the stamp names whatever
	// the workers ended up failing on however this function returns.
	var outcomes []syncOutcome
	defer func() { record(err == nil, failedPackageNames(outcomes)) }()

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("sync --if-needed: %w", err)
	}

	outcomes, err = executeSync(ctx, cc, s, out, host)
	return err
}

func executeSync(
	ctx context.Context, cc *cmdContext, s syncRun, out *output.Output, host string,
) ([]syncOutcome, error) {
	if s.BuildOnly {
		cc.Installer.SourceOnly = true
	}

	cfg, err := cc.LoadConfig()
	if err != nil {
		return nil, err
	}

	if len(cfg.Packages) == 0 {
		// Deliberately not an early return. An emptied manifest is
		// exactly the state that must still be classified against the
		// lock: with a lock naming roots, this is a stale lock and the
		// sync must refuse rather than report success. Unlocked, the
		// generation still has to be rebuilt, or the last package's
		// symlinks stay active in current/bin.
		out.Info("No packages to sync.")
	}

	lv, err := syncLockView(cc.GalePath)
	if err != nil {
		return nil, err
	}

	plan, warn, err := syncPlan(ctx, cc, cfg, lv, host)
	if err != nil {
		return nil, err
	}
	if warn != "" {
		out.Warn(warn)
	}
	if s.BuildOnly {
		if err := rejectSourceOnly(plan); err != nil {
			return nil, err
		}
	}
	// Under a plan the installer stops resolving anything: versions,
	// artifacts, methods and dependency edges all come from the lock.
	cc.Installer.Plan = plan

	items, err := sortedSyncItems(cfg.Packages, plan)
	if err != nil {
		return nil, err
	}
	outcomes := collectSyncOutcomes(ctx, cc, items)

	installed, failures := reportSyncOutcomes(out, outcomes, dryRun)

	configChanged := syncDrifted(driftQuery{
		galeDir:    cc.GaleDir,
		storeRoot:  cc.StoreRoot,
		declared:   cfg.Packages,
		plan:       plan,
		pinResolve: versionedRecipeResolverWith(cc, ctx),
	})

	return outcomes, finishAndReport(ctx, out, syncFinish{
		dryRun:        dryRun,
		failures:      failures,
		installed:     installed,
		configChanged: configChanged,
		locked:        plan != nil,
	}, rebuildForSync(cc, plan), len(cfg.Packages)-installed)
}

// rebuildForSync picks the generation rebuild this sync must use.
//
// A locked sync activates its plan, not the manifest. The canonical
// rebuild resolves bare pins through recipes, which under a lock would
// select a version the lock never named, and it passes no revalidation
// callback, which leaves design §6's window open.
func rebuildForSync(ctx *cmdContext, plan *lockplan.Plan) func() error {
	if plan != nil {
		return ctx.RebuildGenerationLocked
	}
	return ctx.RebuildGenerationCanonical
}

// finishAndReport rebuilds the generation and turns the run's failures
// into the command's exit status and its closing line.
//
// A rebuild failure with no install failures is the generation's own
// error and says so; with install failures the workers' errors are
// what the user must act on, so they propagate unwrapped and the
// warning only counts them.
func finishAndReport(
	ctx context.Context, out *output.Output, f syncFinish, rebuild func() error, upToDate int,
) error {
	if err := finishSync(ctx, f, rebuild); err != nil {
		if len(f.failures) > 0 {
			out.Warn(fmt.Sprintf(
				"Sync finished with %d error(s)", len(f.failures),
			))
			return err
		}
		return fmt.Errorf("rebuild generation: %w", err)
	}

	out.Success(fmt.Sprintf(
		"Sync complete: %d installed, %d up to date",
		f.installed, upToDate,
	))
	return nil
}

// retargetSync points ctx at an explicit project directory.
//
// The explicit directory takes precedence over scope flags; syncIfNeeded
// supplies it when shell/run are invoked with --project. Registration
// happens at publication (rebuildGenerationWith), not here.
func retargetSync(ctx *cmdContext, projectDir string) {
	if projectDir != "" {
		ctx.GalePath = filepath.Join(projectDir, "gale.toml")
		ctx.GaleDir = filepath.Join(projectDir, ".gale")
	}
}

// syncPlan builds the locked plan for this run, returning the plan
// (nil when the sync is unlocked) and any advisory warning.
//
// The whole plan resolves before the first install runs. That ordering
// is the point: a locked sync that cannot describe its complete
// closure must abandon the operation while abandoning it is still
// free, having touched neither the store nor the generation
// (design §4).
func syncPlan(
	ctx context.Context, cc *cmdContext, cfg *config.GaleConfig, lv *lockfile.View, host string,
) (*lockplan.Plan, string, error) {
	return lockedSyncPlan(lv, lockplan.Request{
		Host:     host,
		Platform: currentPlatform(),
		Declared: cfg.Packages,
		// So a stale-lock refusal names the section that actually
		// supplies each disagreeing pin, which for a host overlay is
		// not the target the lock roots it in.
		DeclaredOrigin: cfg.PackageOrigins(host),
		Resolve:        versionedRecipeResolverWith(cc, ctx),
	}, syncNoFrozen)
}

// syncLockView loads the lock this sync will enforce.
//
// Skipped entirely under --no-frozen. The flag means the lock has no
// authority over this run, so a file that cannot even be parsed must
// not fail the command either — loading it first and deciding
// afterwards would leave one class of lock unbypassable.
func syncLockView(galePath string) (*lockfile.View, error) {
	if syncNoFrozen {
		return &lockfile.View{Kind: lockfile.KindAbsent}, nil
	}
	lp, err := lockfilePath(galePath)
	if err != nil {
		return nil, err
	}
	lv, err := lockfile.Load(lp)
	if err != nil {
		return nil, fmt.Errorf("reading lockfile: %w", err)
	}
	return lv, nil
}

// reportSyncOutcomes emits every per-package line and returns the
// install count plus the failures themselves. Split out of runSync so
// the outcome vocabulary lives in one place; runSyncOne prints
// nothing, so this is the only thing that decides what a sync
// looks like.
//
// The errors are returned rather than counted because the exit code
// depends on them: a count reaching finishSync makes every worker
// failure exit 1, including the integrity conflicts the whole lock
// exists to detect.
func reportSyncOutcomes(
	out *output.Output, outcomes []syncOutcome, dryRun bool,
) (installed int, failures []error) {
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
		case o.resolveErr != nil:
			out.Warn(fmt.Sprintf(
				"%s@%s: %v. "+
					"Run 'gale update %s' to install latest.",
				name, version, o.resolveErr, name,
			))
			failures = append(failures, o.resolveErr)
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
			failures = append(failures, o.installErr)
		case dryRun:
			// After the error cases, not before them. Under a lock the
			// dry run verifies provenance and can conflict; reporting
			// that as a planned install would promise a clean sync
			// minutes before the real one exits 3.
			out.Info(fmt.Sprintf(
				"install %s@%s (stale)", name, version,
			))
			installed++
		default:
			if o.stale {
				out.Info(fmt.Sprintf(
					"%s@%s stale — deps changed; reinstalling",
					name, version,
				))
			}
			out.Info(fmt.Sprintf("Installing %s@%s...",
				name, version))
			reportResult(out, o.result, "Installed", "built from source")
			installed++
		}
	}
	return installed, failures
}

// driftQuery is syncDrifted's inputs.
type driftQuery struct {
	galeDir, storeRoot string
	declared           map[string]string
	// plan selects which question is asked. Non-nil means the active
	// generation is compared against the lock; nil means against the
	// manifest resolved through recipes.
	plan       *lockplan.Plan
	pinResolve versionedRecipeResolver
}

// syncDrifted answers whether the active generation needs rebuilding,
// choosing the question by whether the sync is locked.
//
// The two are mutually exclusive rather than one computed and then
// overwritten. Running the unlocked check under a plan costs a
// registry round trip per bare pin, immediately after plan
// construction already resolved every recipe the operation needs, and
// it leaves the locked answer one deleted line away from being the
// unused one.
func syncDrifted(q driftQuery) bool {
	if q.plan != nil {
		if lockedGenerationDrifted(q.galeDir, q.storeRoot, q.plan) {
			return true
		}
		want, err := lockedRebuildPackages(q.plan)
		if err != nil {
			return true
		}
		// Package versions can match while the shared farm is
		// wrong. Walk the lock's roots, not gale.toml's bare
		// pins: FarmStoreDirs floats a bare pin to the highest
		// on-disk revision, which can hide farm drift for the
		// locked generation.
		return farmDrifted(want, q.storeRoot)
	}
	if generationDrifted(
		q.galeDir, q.storeRoot, q.declared, q.pinResolve,
	) {
		return true
	}
	// Package versions can match while the shared farm is wrong.
	// finishSync skips the rebuild unless this is true, and that
	// skip made "Run: gale sync" a no-op for farm drift after
	// doctor --repair went away.
	return farmDrifted(q.declared, q.storeRoot)
}

// farmDrifted reports whether this scope's farm closure is missing
// or broken. Extra healthy entries from another scope are not
// drift (CheckDrift type 1 only flags broken links).
func farmDrifted(pkgs map[string]string, storeRoot string) bool {
	issues, err := farm.CheckDrift(
		generation.FarmStoreDirs(pkgs, storeRoot),
		farm.DirFromStoreRoot(storeRoot),
	)
	return err != nil || len(issues) > 0
}

// lockedGenerationDrifted is generationDrifted's counterpart under a
// plan: it compares the active generation against the versions the
// LOCK names, never against what a recipe currently offers.
//
// The distinction decides whether a no-op sync rebuilds, and getting
// it wrong is silent. gale.toml pins bare versions, so CheckDeclared
// accepts a manifest pin of "1.7" against a locked "1.7-1" and against
// an active "1.7-2" alike. Asking the recipe resolver then reports "the
// active generation matches the current recipe" and the rebuild is
// skipped, leaving a version on PATH that the lock does not describe
// and that nothing else in the command will notice, because there was
// nothing to install and therefore no failure to report.
func lockedGenerationDrifted(
	galeDir, storeRoot string, plan *lockplan.Plan,
) bool {
	want, err := lockedRebuildPackages(plan)
	if err != nil {
		return true
	}
	active, err := generation.CurrentVersionsStrict(galeDir, storeRoot)
	if err != nil {
		// Prefer a wasted rebuild to a stale PATH; the rebuild itself
		// surfaces a follow-on error if the state is broken.
		//
		// Strict read, tolerant answer (gh#210). The lenient reader
		// never reaches this branch: it reports a generation it could
		// not walk as an empty one, which against a plan rooting
		// nothing compares EQUAL, so the sync skips the rebuild and
		// no one ever learns the walk failed. Strictness makes the
		// failure visible; aborting on it would strand a user whose
		// generation is broken in the one command that repairs it.
		return true
	}
	if len(active) != len(want) {
		return true
	}
	// Through ActiveVersions for the same reason the unlocked path uses
	// it: the generation records the store-dir basename it actually
	// linked, which for a pre-revision install is the bare directory
	// rather than the canonical name.
	for name, version := range generation.ActiveVersions(want, storeRoot) {
		if active[name] != version {
			return true
		}
	}
	return false
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
	active, err := generation.CurrentVersionsStrict(galeDir, storeRoot)
	if err != nil {
		// On read error, prefer to rebuild — better a wasted
		// gen than a stale PATH. The rebuild itself will
		// surface a follow-on error if the state is broken.
		//
		// Strict for the reason lockedGenerationDrifted is: an
		// emptied manifest is exactly the state whose rebuild must
		// still run, and it is exactly the state an unreadable
		// generation compares equal to under the lenient reader
		// (gh#210). The answer stays true either way — sync is the
		// repair, so it must never refuse to run in a broken scope.
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
// gale.toml) or that the shared farm drifted while versions
// still match. The rebuild must run even when nothing was
// installed; otherwise the removed package's symlink stays
// active in current/bin, and farm drift stays broken.
//
// locked signals that the sync ran from a plan. Under a lock the plan
// is one unit: design §8 skips the rebuild on ANY failure, not only an
// integrity one, because activating the nodes that happened to verify
// publishes a generation the lock never describes. Issue #20's partial
// rebuild survives unlocked, where losing the whole PATH to one broken
// recipe is the worse trade.
func finishSync(ctx context.Context, f syncFinish, rebuild func() error) error {
	if f.dryRun {
		return nil
	}
	failed := len(f.failures)
	if failed > 0 && f.locked {
		return fmt.Errorf(
			"%d package(s) could not be synced; the generation was left "+
				"unchanged because gale.lock is enforced: %w",
			failed, errors.Join(f.failures...),
		)
	}
	if f.installed == 0 && failed == 0 && !f.configChanged {
		// Genuine no-op: every package was already up to date.
		// A deadline that expired after that classification must
		// not stamp incomplete or withhold the next --if-needed.
		return nil
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("sync --if-needed: %w", err)
	}
	rebuildErr := rebuild()
	if failed > 0 {
		if rebuildErr != nil {
			return fmt.Errorf("%d package(s) could not be synced: %w; rebuild: %w",
				failed, errors.Join(f.failures...), rebuildErr)
		}
		return fmt.Errorf("%d package(s) could not be synced: %w",
			failed, errors.Join(f.failures...))
	}
	return rebuildErr
}

// syncFinish is finishSync's inputs. A struct rather than six
// positional parameters: the four counters and flags are all bool or
// int, so a transposed pair at a call site would compile and silently
// invert the rebuild decision.
// failures carries the workers' errors rather than a count. A count
// forced finishSync to invent a fresh error, which discarded every
// sentinel the workers produced and sent a cached provenance conflict
// out as exit 1 — the taxonomy's most important case, misclassified at
// the very last step.
type syncFinish struct {
	dryRun        bool
	failures      []error
	installed     int
	configChanged bool
	locked        bool
}

// syncItem is the per-package unit of work passed to runSyncOne.
type syncItem struct {
	name, version string
	// planned is the lock's node for this package, or nil when the
	// sync is unlocked. Non-nil makes the lock the exclusive selector:
	// the version, the recipe and the method all come from here, and
	// runSyncOne resolves nothing of its own.
	planned *lockplan.Node
}

// syncOutcome is the result of one runSyncOne call. It is
// pure data — the caller emits all user-visible output after
// the serial loop so lines are printed in a deterministic
// order.
type syncOutcome struct {
	name, version string
	upToDate      bool
	stale         bool
	resolveErr    error
	installErr    error
	result        *installer.InstallResult
}

// sortedSyncItems converts cfg.Packages to a syncItem slice ordered by
// name, each carrying its locked node when the sync is locked. Used by
// runSync so per-package output is emitted in a stable order.
//
// It no longer reads the lockfile directly. The per-package entry
// lookup existed to feed the SHA-changed warning, and that warning is
// gone: under a plan the installer enforces the hash rather than
// remarking on it, and without a plan the lock has no authority to
// remark with.
func sortedSyncItems(
	pkgs map[string]string, plan *lockplan.Plan,
) ([]syncItem, error) {
	names := make([]string, 0, len(pkgs))
	for name := range pkgs {
		names = append(names, name)
	}
	sort.Strings(names)
	items := make([]syncItem, len(names))
	for i, name := range names {
		items[i] = syncItem{name: name, version: pkgs[name]}
		if plan == nil {
			continue
		}
		// Plan construction already proved the locked roots and the
		// declared packages agree exactly (lockfile.CheckDeclared), so
		// a declared name with no node cannot occur; treating it as
		// unlocked would silently install one package off-lock.
		n, found := plan.ForName(name)
		if !found {
			return nil, fmt.Errorf(
				"%w: %s is declared in gale.toml but the plan has no node for it",
				lockfile.ErrMissingNode, name,
			)
		}
		items[i].planned = &n
		// The lock is the version selector, so the reported version is
		// the canonical one it names, not gale.toml's bare pin.
		items[i].version = n.Version
	}
	return items, nil
}

// runSyncOne is the per-package body of sync.
// Pure with respect to output (no fmt.Println / out.Info calls);
// caller emits user-visible lines after the serial loop.
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
// rather than churn. A canceled or deadline-exceeded resolve is not
// offline: it returns the context error so the package is not marked
// up to date.
func installedStale(ctx context.Context, cc *cmdContext, w syncItem) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	r, err := resolveRecipeForPin(ctx, w.name, w.version, cc.Resolver, cc.Registry)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return false, ctxErr
		}
		storeDir, ok := cc.Installer.Store.StorePath(w.name, w.version)
		return ok && !depsmeta.Has(storeDir), nil
	}

	// Prefer the recipe's canonical dir; fall back to the bare
	// resolution when that revision is not yet on disk.
	storeDir, ok := cc.Installer.Store.StorePath(w.name, r.Package.Full())
	if !ok {
		storeDir, ok = cc.Installer.Store.StorePath(w.name, w.version)
	}
	if !ok {
		return false, nil
	}
	if !depsmeta.Has(storeDir) {
		// Pre-revision install — soft migration: mark stale.
		return true, nil
	}
	stale, staleErr := installer.IsStale(ctx, installer.StaleQuery{
		StoreDir: storeDir, Recipe: r,
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Resolver: cc.Resolver,
	})
	if staleErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return false, ctxErr
		}
		return false, nil
	}
	return stale, nil
}

// runSyncOneLocked is the per-package body under a plan.
//
// It is a separate function rather than branches inside runSyncOne
// because almost every step of the unlocked body is a selector the
// lock replaces. Resolution is gone: the plan carries the validated
// recipe. The staleness check is gone: design §4 makes the cache
// question an attestation question, which installLocked answers by
// verifying the store directory's provenance against the locked graph
// — a check that also catches the tampering IsStale was never looking
// for. Reinstall is gone: §4 permits committing only ABSENT canonical
// directories, so an occupied one either attests the lock or is a
// conflict, and replacing it in-plan is prohibited.
func runSyncOneLocked(ctx context.Context, cc *cmdContext, w syncItem, dryRun bool) syncOutcome {
	outcome := syncOutcome{name: w.name, version: w.version}

	if dryRun {
		// Read-only, and it runs the same verification the real sync
		// would: a dry run that reports "up to date" for a directory
		// the sync would reject with an integrity error is worse than
		// no dry run at all.
		hit, err := cc.Installer.VerifyPlanned(w.name)
		if err != nil {
			outcome.installErr = err
			return outcome
		}
		outcome.upToDate = hit
		return outcome
	}

	result, err := cc.Installer.Install(ctx, w.planned.Recipe)
	if err != nil {
		outcome.installErr = err
		return outcome
	}
	outcome.result = result
	// A verified cache hit is reported as up to date, not as an
	// install: nothing was written. The verification it passed is
	// stronger than the unlocked path's, so the quiet line is earned.
	outcome.upToDate = result.Method == installer.MethodCached
	return outcome
}

func collectSyncOutcomes(ctx context.Context, cc *cmdContext, items []syncItem) []syncOutcome {
	outcomes := make([]syncOutcome, 0, len(items))
	for i, w := range items {
		if err := ctx.Err(); err != nil {
			for _, rest := range items[i:] {
				outcomes = append(outcomes, syncOutcome{
					name: rest.name, version: rest.version, installErr: err,
				})
			}
			break
		}
		outcomes = append(outcomes, runSyncOne(ctx, cc, w, dryRun))
	}
	return outcomes
}

func runSyncOne(ctx context.Context, cc *cmdContext, w syncItem, dryRun bool) syncOutcome {
	if w.planned != nil {
		return runSyncOneLocked(ctx, cc, w, dryRun)
	}

	outcome := syncOutcome{name: w.name, version: w.version}

	// Step a/b: check if already installed and whether stale.
	if cc.Installer.Store.IsInstalled(w.name, w.version) {
		stale, err := installedStale(ctx, cc, w)
		if err != nil {
			outcome.installErr = err
			return outcome
		}
		outcome.stale = stale

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
	r, err := resolveRecipeForPin(ctx, w.name, w.version, cc.Resolver, cc.Registry)
	if err != nil {
		outcome.resolveErr = err
		return outcome
	}

	// Step f: install or reinstall.
	var result *installer.InstallResult
	if outcome.stale {
		result, err = cc.Installer.Reinstall(ctx, r)
	} else {
		result, err = cc.Installer.Install(ctx, r)
	}
	if err != nil {
		outcome.installErr = err
		return outcome
	}
	outcome.result = result

	// Sync never writes gale.lock (design §11). It is a pure
	// consumer of one: the lock says what to install, so a sync
	// that also wrote it would ratify whatever it happened to
	// install and there would be nothing left to enforce.
	return outcome
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
	syncCmd.Flags().BoolVar(&syncNoFrozen, "no-frozen", false,
		"Ignore gale.lock and install from recipes without integrity enforcement")
	syncCmd.Flags().BoolVar(&syncOnlyIfNeeded, "if-needed", false,
		"Sync only when the last sync did not complete on these inputs")
	rootCmd.AddCommand(syncCmd)
}
