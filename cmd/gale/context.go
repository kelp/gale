package main

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/kelp/gale/internal/attestation"
	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/farm"
	"github.com/kelp/gale/internal/filelock"
	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/lockwrite"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/parallel"
	"github.com/kelp/gale/internal/projects"
	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/registry"
	"github.com/kelp/gale/internal/store"
	"github.com/kelp/gale/internal/timing"
)

// cmdContext holds resolved config, store, and installer
// shared by sync, update, and install commands.
type cmdContext struct {
	GalePath  string // path to gale.toml
	GaleDir   string // .gale directory (project or global)
	StoreRoot string
	Resolver  installer.RecipeResolver
	Installer *installer.Installer
	Registry  *registry.Registry // nil when --recipes

	// Parallelism is the resolved download/sync concurrency
	// (GALE_JOBS > [sync] parallelism > default). It sizes both
	// the Installer's Downloads limiter and sync's worker pool,
	// so one configured number bounds total in-flight downloads.
	Parallelism int

	// Host force-writes the package to
	// [hosts.<Host>.packages] when finalize runs. Empty
	// preserves the existing location (see WriteConfig).
	Host string

	// lockTargets is the lock work this command has accumulated:
	// one entry per manifest section it wrote, mapping package
	// name to the canonical version-revision verified for it. A
	// section with no roots is a section the command emptied.
	//
	// The lock is written once, at the end of the command, because
	// a target's roots are resolved into one closure and one
	// document (see WriteLock). Writing per package would resolve
	// the first root against a manifest the later ones had not
	// landed in yet, and would leave a failed second package with
	// the first already locked.
	lockTargets map[string]map[string]string
}

// newCmdContext resolves the config, store, and installer.
// When recipesPath is non-empty, recipes are resolved locally
// from that directory (--recipes always requires a value).
//
// Scope flags: when both global and project are false, the
// current auto-detect behavior is used (project gale.toml
// first, then global). When global is true, uses the global
// config path. When project is true, uses the project config
// path (errors if no project found).
func newCmdContext(recipesPath string, global, project bool) (*cmdContext, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("getting working dir: %w", err)
	}

	// Resolve config path based on scope flags.
	var galePath string
	if global || project {
		useGlobal := resolveScope(global, project, cwd)
		if !useGlobal {
			if _, err := projectConfigPath(cwd); err != nil {
				return nil, errors.New(errNoProject)
			}
		}
		galePath, err = resolveConfigPath(useGlobal)
		if err != nil {
			return nil, fmt.Errorf("resolving config path: %w", err)
		}
	} else {
		// Auto-detect: project config first, then global.
		galePath, err = projectConfigPath(cwd)
		if err != nil {
			globalDir, dirErr := galeConfigDir()
			if dirErr != nil {
				return nil, dirErr
			}
			galePath = filepath.Join(globalDir, "gale.toml")
		}
	}

	// Resolve galeDir from configPath.
	galeDir, err := galeDirForConfig(galePath)
	if err != nil {
		return nil, fmt.Errorf("resolving gale dir: %w", err)
	}

	// Record project-scoped use in the machine-local project
	// registry so gc can retain this project's generation when
	// run from anywhere else (gh#115).
	registerProject(galePath)

	// Set up resolver.
	storeRoot := defaultStoreRoot()
	resolver, reg, resolveErr := resolveRecipeResolver(recipesPath)
	if resolveErr != nil {
		return nil, resolveErr
	}

	// Resolve download/sync concurrency once. A failure to read
	// config.toml (e.g. absent on first run) is not fatal:
	// ResolveParallelism falls back to GALE_JOBS or the default.
	appCfg, _ := loadAppConfig()
	n := config.ResolveParallelism(appCfg)

	inst := &installer.Installer{
		Store:     store.NewStore(storeRoot),
		Resolver:  resolver,
		Verifier:  attestation.NewVerifier(),
		Downloads: parallel.NewLimiter(n),
		// Cross-project farm claimant guard (design §4): every
		// farm population this installer performs is checked
		// against the other scopes' claims first. The initiating
		// scope (galeDir) is excluded — its claim is the install
		// itself, and its old closure is superseded, not a veto.
		FarmGuard: func(placements []farm.Placement) error {
			// The initiating scope claims the closure this install
			// leaves it with, so a second version of a package one
			// of its own binaries links is refused rather than
			// silently repointing the shared soname under it. Its
			// OLD closure is never passed: that would be the verb
			// veto §4 forbids, deadlocking the scope against its
			// own update.
			//
			// One commit at a time, which is not the same as the
			// whole operation. A sync installs every root before it
			// rebuilds, so each call here sees the pre-sync
			// generation and cannot see a conflict between two roots
			// that only meet in the final closure. P7 replaces this
			// with one guarded batch over the whole plan; see
			// ProposedClaimant's own note.
			self, err := generation.ProposedClaimant(
				generation.ChangedBy(placements), galeDir, storeRoot,
			)
			if err != nil {
				return err
			}
			return farm.GuardPopulate(
				placements,
				append(
					generation.FarmClaimants(storeRoot, galeDir),
					self,
				),
			)
		},
	}

	return &cmdContext{
		GalePath:    galePath,
		GaleDir:     galeDir,
		StoreRoot:   storeRoot,
		Resolver:    resolver,
		Installer:   inst,
		Registry:    reg,
		Parallelism: n,
	}, nil
}

// registerProject records the project owning configPath in the
// machine-local registry (~/.gale/projects) so gc can union
// this project's generation into its retention set (gh#115).
// No-ops for global configs and dry runs. Best-effort by
// design: a read-only gale home must never block the command
// that triggered registration.
func registerProject(configPath string) {
	if dryRun || configPath == "" {
		return
	}
	globalDir, err := galeConfigDir()
	if err != nil {
		return
	}
	if configInGaleDir(configPath, globalDir) {
		return // the global config is not a project
	}
	if err := projects.Register(
		globalDir, filepath.Dir(configPath),
	); err != nil {
		fmt.Fprintf(os.Stderr,
			"warning: registering project for gc: %v\n", err)
	}
}

// readConfigOrToolVersions reads configPath and parses it
// as a GaleConfig. If gale.toml is absent, falls back to
// .tool-versions in the same directory. If that is also
// absent, returns an empty GaleConfig. Callers apply
// host-specific flattening after this returns.
func readConfigOrToolVersions(configPath string) (*config.GaleConfig, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("reading %s: %w", configPath, err)
		}
		// Fallback: .tool-versions in the same directory.
		tvPath := filepath.Join(filepath.Dir(configPath), ".tool-versions")
		tvData, tvErr := os.ReadFile(tvPath)
		if tvErr != nil {
			if errors.Is(tvErr, os.ErrNotExist) {
				return &config.GaleConfig{Packages: map[string]string{}}, nil
			}
			return nil, fmt.Errorf("reading .tool-versions: %w", tvErr)
		}
		pkgs, pkgErr := config.ParseToolVersions(string(tvData))
		if pkgErr != nil {
			return nil, fmt.Errorf("parsing .tool-versions: %w", pkgErr)
		}
		return &config.GaleConfig{Packages: pkgs}, nil
	}
	cfg, err := config.ParseGaleConfig(string(data))
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

// configOrToolVersionsExists reports whether the resolved
// config path has anything to read: the gale.toml itself, or
// the project's .tool-versions sibling that
// readConfigOrToolVersions falls back to when gale.toml is
// absent (projectConfigPath returns the would-be gale.toml
// path for .tool-versions-only trees). Used by the sbom and
// list --all project legs.
func configOrToolVersionsExists(path string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return true, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	tv := filepath.Join(filepath.Dir(path), ".tool-versions")
	if _, err := os.Stat(tv); err == nil {
		return true, nil
	}
	return false, nil
}

// LoadConfig reads and parses the gale.toml that this
// context points to. If gale.toml doesn't exist, falls
// back to reading .tool-versions in the same directory.
func (ctx *cmdContext) LoadConfig() (*config.GaleConfig, error) {
	cfg, err := readConfigOrToolVersions(ctx.GalePath)
	if err != nil {
		return nil, err
	}
	cfg.ApplyHost(config.CurrentHost())
	return cfg, nil
}

// versionedRecipeResolver fetches a recipe for a specific
// config pin. Nil means fall back to store bare-version
// resolution (highest revision on disk).
type versionedRecipeResolver func(name, version string) (*recipe.Recipe, error)

func (ctx *cmdContext) versionedRecipeResolver() versionedRecipeResolver {
	if ctx == nil || ctx.Resolver == nil {
		return nil
	}
	resolve := ctx.Resolver
	reg := ctx.Registry
	return func(name, version string) (*recipe.Recipe, error) {
		return resolveRecipeForPin(name, version, resolve, reg)
	}
}

// resolveRecipeForPin fetches the recipe for a config pin.
// Shared by sync staleness, generation rebuild, and gc
// retention so they all agree on the canonical revision.
func resolveRecipeForPin(
	name, version string,
	resolve installer.RecipeResolver,
	reg *registry.Registry,
) (*recipe.Recipe, error) {
	r, err := resolve(name)
	if err == nil &&
		(r.Package.Version == version || r.Package.Full() == version) {
		return r, nil
	}
	if reg != nil {
		var pinned *recipe.Recipe
		pinned, vErr := reg.FetchRecipeVersion(name, version)
		if vErr == nil {
			return pinned, nil
		}
		if err != nil {
			return nil, fmt.Errorf(
				"resolving %s@%s: %w", name, version, err,
			)
		}
		return nil, fmt.Errorf(
			"%s@%s not found (registry has %s): %w",
			name, version, r.Package.Version, vErr,
		)
	}
	if err != nil {
		return nil, fmt.Errorf(
			"resolving %s@%s: %w", name, version, err,
		)
	}
	return nil, fmt.Errorf(
		"%s@%s not found (registry has %s)",
		name, version, r.Package.Version,
	)
}

// canonicalizeForBuild maps bare config pins to the recipe's
// canonical `<version>-<revision>` so generation rebuild links
// the revision the recipe offers, not a higher orphan left on
// disk by a withdrawn recipe revision (gh#137).
func canonicalizeForBuild(
	pkgs map[string]string,
	pinResolve versionedRecipeResolver,
) map[string]string {
	if pinResolve == nil || len(pkgs) == 0 {
		return pkgs
	}
	out := make(map[string]string, len(pkgs))
	for name, version := range pkgs {
		out[name] = version
		if store.HasNumericRevisionSuffix(version) {
			continue
		}
		r, err := pinResolve(name, version)
		if err != nil || r.Package.Version != version {
			continue
		}
		out[name] = r.Package.Full()
	}
	return out
}

// isSupersededRevision reports whether a store revision exceeds
// the revision the recipe currently offers for the same version.
func isSupersededRevision(
	name, version string,
	pinResolve versionedRecipeResolver,
) bool {
	if pinResolve == nil || !store.HasNumericRevisionSuffix(version) {
		return false
	}
	base, rev := store.SplitRevision(version)
	if rev <= 0 {
		return false
	}
	r, err := pinResolve(name, base)
	if err != nil {
		return false
	}
	recipeRev := r.Package.Revision
	if recipeRev <= 0 {
		recipeRev = 1
	}
	return rev > recipeRev
}

// generationLinksSupersededOrphan reports whether the active
// generation still symlinks a store revision higher than the
// recipe's current revision while config uses a bare pin for
// that package (gh#137). Explicit revision pins that match the
// active dir are user intent and must not trigger a rebuild.
func generationLinksSupersededOrphan(
	galeDir, storeRoot, configPath string,
	pinResolve versionedRecipeResolver,
) bool {
	if pinResolve == nil || galeDir == "" {
		return false
	}
	pkgs, err := readConfigPackages(configPath)
	if err != nil {
		return false
	}
	active, err := generation.CurrentVersions(galeDir, storeRoot)
	if err != nil {
		return false
	}
	for name, activeVer := range active {
		if !isSupersededRevision(name, activeVer, pinResolve) {
			continue
		}
		pin, ok := pkgs[name]
		if !ok {
			continue
		}
		if store.HasNumericRevisionSuffix(pin) && pin == activeVer {
			continue
		}
		return true
	}
	return false
}

// storeRetentionKey returns the name@version key gc should
// retain for a config pin. Bare pins prefer the recipe's
// canonical revision over the highest revision on disk.
func storeRetentionKey(
	s *store.Store,
	name, version string,
	pinResolve versionedRecipeResolver,
) string {
	if store.HasNumericRevisionSuffix(version) {
		if dir, ok := s.StorePath(name, version); ok {
			return name + "@" + filepath.Base(dir)
		}
		return name + "@" + version
	}
	if pinResolve != nil {
		r, err := pinResolve(name, version)
		if err == nil && r.Package.Version == version {
			canon := r.Package.Full()
			if dir, ok := s.StorePath(name, canon); ok {
				return name + "@" + filepath.Base(dir)
			}
			return name + "@" + canon
		}
	}
	if dir, ok := s.StorePath(name, version); ok {
		return name + "@" + filepath.Base(dir)
	}
	return name + "@" + version
}

// rebuildGeneration reads the effective project/global
// config and rebuilds the generation symlinks. Packages
// whose store dir is missing are silently skipped with a
// warning: gale.toml legitimately lists packages that
// aren't installed locally — `gale add` without sync, a
// fresh clone on a new host, an unsupported-platform skip
// — and failing the rebuild would desync PATH from config
// (gh#68). The skipped package is reported on stderr by
// generation.Build. Bare config pins are first mapped to
// the recipe's canonical revision via pinResolve (gh#137,
// see canonicalizeForBuild); a nil pinResolve skips that.
func rebuildGeneration(
	galeDir, storeRoot, configPath string,
	pinResolve versionedRecipeResolver,
) error {
	pkgs, err := readConfigPackages(configPath)
	if err != nil {
		return err
	}
	pkgs = canonicalizeForBuild(pkgs, pinResolve)
	// nil: no plan to revalidate here yet — P7 threads a locked
	// sync's validation callback through rebuildGeneration itself.
	if err := generation.BuildWithValidate(pkgs, galeDir, storeRoot, nil); err != nil {
		return err
	}
	autoPruneGenerations(galeDir, storeRoot)
	return nil
}

// autoPruneGenerations is the post-Build hook that bounds gen
// dir accumulation. Every command that creates a generation
// (install, update, sync, add, remove, recipe install, ...)
// routes through rebuildGeneration, so a single hook here
// covers all of them. Reads the keep
// count from ~/.gale/config.toml [generation] keep, defaults
// to DefaultGenerationKeep when unset, treats negative as
// "disabled."
//
// Errors during prune are surfaced as warnings, not failures —
// the install / sync that triggered this already succeeded and
// must not regress because of a cleanup hiccup. Removed gen
// numbers are reported on stderr so users see what happened.
func autoPruneGenerations(galeDir, storeRoot string) {
	keep := loadGenerationKeep()
	if keep <= 0 {
		return
	}
	removed, err := generation.PruneOldGenerations(galeDir, storeRoot, keep)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"auto-gc: prune failed: %v\n", err)
		return
	}
	if len(removed) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr,
		"auto-gc: pruned %d old generation(s) (kept last %d): %s\n",
		len(removed), keep, formatGenList(removed))
}

// loadGenerationKeep returns the auto-gc retention from
// ~/.gale/config.toml [generation] keep. Missing or unreadable
// config falls back to DefaultGenerationKeep so a user who
// has never created config.toml still gets bounded gen growth.
//
// Reads from the global config regardless of caller scope
// since gen retention is an app-level concern, not per-project.
func loadGenerationKeep() int {
	cfg, err := loadAppConfig()
	if err != nil {
		return config.DefaultGenerationKeep
	}
	return cfg.Generation.EffectiveGenerationKeep()
}

// formatGenList renders a sorted ascending slice of gen numbers
// as a comma-separated list, collapsing runs of consecutive ids
// into "lo-hi" ranges so a busy auto-gc message stays readable
// when pruning, say, 30 sequential gens.
func formatGenList(nums []int) string {
	if len(nums) == 0 {
		return ""
	}
	var parts []string
	start := nums[0]
	prev := nums[0]
	for _, n := range nums[1:] {
		if n == prev+1 {
			prev = n
			continue
		}
		parts = append(parts, formatRange(start, prev))
		start = n
		prev = n
	}
	parts = append(parts, formatRange(start, prev))
	return strings.Join(parts, ", ")
}

func formatRange(lo, hi int) string {
	if lo == hi {
		return strconv.Itoa(lo)
	}
	return strconv.Itoa(lo) + "-" + strconv.Itoa(hi)
}

func readConfigPackages(configPath string) (map[string]string, error) {
	cfg, err := loadEffectiveConfig(configPath)
	if err != nil {
		return nil, err
	}
	pkgs := cfg.Packages
	if pkgs == nil {
		pkgs = map[string]string{}
	}
	return pkgs, nil
}

func loadEffectiveConfig(configPath string) (*config.GaleConfig, error) {
	cfg, err := readConfigOrToolVersions(configPath)
	if err != nil {
		return nil, err
	}
	host := config.CurrentHost()
	cfg.Packages = cfg.EffectivePackages(host)
	cfg.Pinned = cfg.EffectivePinned(host)
	return cfg, nil
}

func projectConfigPath(cwd string) (string, error) {
	if path, err := config.FindGaleConfig(cwd); err == nil {
		return path, nil
	}
	if tv := config.FindToolVersions(cwd); tv != "" {
		return filepath.Join(filepath.Dir(tv), "gale.toml"), nil
	}
	return "", config.ErrGaleConfigNotFound
}

// loadAppConfig reads and parses ~/.gale/config.toml.
func loadAppConfig() (*config.AppConfig, error) {
	galeDir, err := galeConfigDir()
	if err != nil {
		return nil, fmt.Errorf("finding config dir: %w", err)
	}
	data, err := os.ReadFile(filepath.Join(galeDir, "config.toml"))
	if err != nil {
		return nil, fmt.Errorf("reading config.toml: %w", err)
	}
	return config.ParseAppConfig(string(data))
}

// resolveBuildDebug resolves the debug mode from CLI
// flags, recipe setting, and config. Precedence:
// CLI flag > recipe > config > default (release).
func resolveBuildDebug(recipeDebug, cliDebug, cliRelease bool) bool {
	// CLI flags override everything.
	if cliRelease {
		return false
	}
	if cliDebug {
		return true
	}

	// Recipe setting.
	if recipeDebug {
		return true
	}

	// Config setting.
	if cfg, err := loadAppConfig(); err == nil {
		return cfg.Build.Debug
	}

	return false
}

// newRegistry creates a Registry, using the URL from
// ~/.gale/config.toml if configured. Wires the package-level
// `dryRun` flag and the `GALE_OFFLINE` environment variable
// (already honoured by registry.New) into the returned
// Registry so the cache contract is uniform across commands.
func newRegistry() *registry.Registry {
	var reg *registry.Registry
	cfg, err := loadAppConfig()
	if err != nil {
		reg = registry.New()
	} else {
		reg = registry.NewWithURL(cfg.Registry.URL)
	}
	reg.DryRun = dryRun
	return reg
}

// lockfilePath returns the gale.lock path for a given
// gale.toml path. Returns an error if configPath does not
// end with ".toml".
func lockfilePath(configPath string) (string, error) {
	if !strings.HasSuffix(configPath, ".toml") {
		return "", fmt.Errorf("config path must end with .toml, got %s", configPath)
	}
	return configPath[:len(configPath)-len(".toml")] + ".lock", nil
}

// noteLockTarget marks a manifest section as needing its lock
// target regenerated without contributing a root. `remove` is the
// caller: it verifies nothing, so every root its targets keep is
// carried from the previous document.
func (ctx *cmdContext) noteLockTarget(target string) {
	if ctx.lockTargets == nil {
		ctx.lockTargets = make(map[string]map[string]string, 1)
	}
	if ctx.lockTargets[target] == nil {
		ctx.lockTargets[target] = make(map[string]string)
	}
}

// noteLockRoot records a root this command verified, under the
// manifest section the package was written to.
func (ctx *cmdContext) noteLockRoot(target, name, version string) {
	ctx.noteLockTarget(target)
	ctx.lockTargets[target][name] = version
}

// WriteLock regenerates every lock target this command touched and
// replaces gale.lock in one atomic write. It is a no-op for a
// command that touched none.
//
// Resolution comes first for every target, the write last. A target
// that fails to resolve must leave the previous gale.lock
// byte-identical, and a run that wrote two manifest sections must
// not land one of them and lose the other.
//
// gale.toml is read exactly once, inside the lock section, and every
// target is resolved against that one parsed manifest. Once, because
// a read per target can straddle a concurrent atomic config write and
// assemble a document from a file that never existed in that state.
// Inside, because a snapshot taken earlier cannot see a manifest
// another command committed in the meantime, and a lock built from it
// disagrees with the file on disk while passing every check this
// writer makes.
//
// It does not serialize the two: gale.toml has its own lock and its
// writers do not take this one. What it buys is that the loser of
// such a race reads the winner's manifest and fails, rather than
// committing a lock the manifest no longer backs. Failing is the
// intended outcome, the same stance as everywhere else here.
//
// No nested file lock, deliberately. Config READ paths take no lock
// at all — only its writers take gale.toml.lock — so reading here
// adds no ordering between the two files. Taking the config lock
// around this section would establish a config→lockfile order that
// any future writer mutating gale.toml inside the section would
// silently invert into an AB-BA deadlock.
func (ctx *cmdContext) WriteLock() error {
	if len(ctx.lockTargets) == 0 {
		return nil
	}
	defer timing.Phase("lockfile-write")()
	lp, err := lockfilePath(ctx.GalePath)
	if err != nil {
		return fmt.Errorf("resolving lockfile path: %w", err)
	}
	return filelock.With(lp+".lock", func() error {
		if beforeLockRead != nil {
			beforeLockRead()
		}
		cfg, err := rawGaleConfig(ctx.GalePath)
		if err != nil {
			return err
		}
		sections := ctx.lockSections(cfg)
		// Absence is nil, which is unlocked mode and the state the
		// first install in a project starts from. Any other failure is
		// a lock that is present and cannot be modeled: rewriting it
		// would discard a document this build does not understand, so
		// it is refused rather than replaced (design §9, §11).
		doc, lockErr := lockfile.ReadV1(lp)
		if lockErr != nil && !errors.Is(lockErr, fs.ErrNotExist) {
			return fmt.Errorf("reading lockfile: %w", lockErr)
		}
		res, err := lockwrite.BuildAll(lockwrite.AllRequest{
			StoreRoot: ctx.StoreRoot,
			Platform:  currentPlatform(),
			Sections:  sections,
			Existing:  doc,
		})
		if err != nil {
			return err
		}
		if res.Doc == nil {
			// Nothing to root and no prior document: a remove in a
			// project that was never locked. Absent stays absent, so an
			// unlocked project is not put into locked mode by a removal,
			// and nothing is named as unlocked either — nothing is stale
			// against a lock that does not exist.
			return nil
		}
		if err := lockfile.WriteV1(lp, res.Doc); err != nil {
			return err
		}
		// After the write lands, never before: what the warning
		// describes is what the NEW lockfile leaves out, and a failed
		// write leaves the previous one intact, omitting nothing.
		warnUnlocked(res.Unlocked)
		return nil
	})
}

// beforeLockRead runs inside WriteLock's critical section, after the
// lockfile lock is held and before the manifest is read. Test seam
// only (nil in production, like beforeGuardedRemoval in remove.go):
// gale.toml is not covered by the lockfile lock, so another command
// can commit a manifest change at exactly this point, and the seam
// makes that moment deterministic instead of a timing race.
var beforeLockRead func()

// lockSections pairs each touched target with the manifest section it
// mirrors, in a deterministic order so a rewrite of an unchanged
// closure produces byte-identical output.
//
// It takes the parsed manifest rather than a path so that resolving
// every section against one read of one file is structural: there is
// no path here that can read gale.toml a second time.
func (ctx *cmdContext) lockSections(cfg *config.GaleConfig) []lockwrite.Section {
	sections := make([]lockwrite.Section, 0, len(ctx.lockTargets))
	for _, target := range slices.Sorted(maps.Keys(ctx.lockTargets)) {
		sections = append(sections, lockwrite.Section{
			Target:   target,
			Roots:    ctx.lockTargets[target],
			Declared: declaredForTarget(cfg, target),
		})
	}
	return sections
}

// declaredForTarget returns the manifest section a lock target
// mirrors: shared [packages] for the default target,
// [hosts.<K>.packages] for a host target.
//
// Raw, never host-merged. Each target roots its own section alone,
// so a reader's EffectiveRoots merge reproduces the manifest's own
// overlay merge. Merging here instead would copy the shared packages
// into every overlay target and lock them twice.
func declaredForTarget(cfg *config.GaleConfig, target string) map[string]string {
	if target == "" {
		return cfg.Packages
	}
	return cfg.Hosts[target].Packages
}

// warnUnlocked reports declared packages the write could back
// neither by fresh verification nor by a carried subgraph. The
// document is complete without them; it is stale against gale.toml,
// which is the same recoverable state `gale add` produces and has
// the same remedy.
//
// One line per package, because `gale install` takes exactly one
// package name and a line naming several is not a command anyone can
// run.
func warnUnlocked(roots []lockwrite.UnlockedRoot) {
	for _, r := range roots {
		fmt.Fprintf(
			os.Stderr,
			"warning: %s is declared but not locked — run: %s\n",
			r.Name, lockRemedy(r),
		)
	}
}

// lockRemedy is the command that puts one unlocked root back into
// the section it is declared in.
//
// A host target's selector is passed to --host verbatim, quoted:
// AddPackage writes to [hosts.<selector>.packages] literally, so a
// wildcard or comma-list selector addresses exactly the section that
// declares the package, and the quoting is what keeps a `*` from
// being expanded by the shell before gale ever sees it. Running it on
// a machine the selector does not match is fine — `install --host`
// for a foreign host is declaration-only and still locks that target.
//
// A selector containing a single quote cannot be spelled that way in
// one line, so the section is named instead of emitting a command
// that would not run. Such a selector is outside the documented
// selector grammar, which gale.toml has always accepted anyway.
func lockRemedy(r lockwrite.UnlockedRoot) string {
	if r.Target == "" {
		return "gale install " + r.Name
	}
	if strings.Contains(r.Target, "'") {
		return fmt.Sprintf(
			"gale install %s --host, with the selector spelled as in "+
				"[hosts.%q.packages]",
			r.Name, r.Target,
		)
	}
	return fmt.Sprintf("gale install %s --host '%s'", r.Name, r.Target)
}

// stripNumericRevision removes a Debian-style "-N" suffix from
// a version string when N is all decimal digits. Pre-release
// tags like "1.0.0-rc1" are left unchanged. Delegates to the
// canonical revision parser in internal/store.
func stripNumericRevision(version string) string {
	base, _ := store.SplitRevision(version)
	return base
}

// addToConfig writes a package version to the given gale.toml
// path. Returns the config path used. When host is non-empty,
// writes to [hosts.<host>.packages]; otherwise writes to the
// shared [packages] section, preserving an existing
// host-scoped entry for the current machine.
//
// configPath is pre-resolved by the caller (once per command
// invocation) so scope is not re-derived on every package.
func addToConfig(name, version, host, configPath string) (string, error) {
	version = stripNumericRevision(version)
	if host != "" {
		if err := config.AddPackage(
			configPath, host, name, version,
		); err != nil {
			return "", fmt.Errorf("adding %s to config: %w", name, err)
		}
		return configPath, nil
	}
	// The written section is discarded: `gale add` is manifest-only
	// and never touches the lock (design §11).
	if _, err := config.UpsertPackage(
		configPath, config.CurrentHost(), name, version,
	); err != nil {
		return "", fmt.Errorf("adding %s to config: %w", name, err)
	}
	return configPath, nil
}

// resolveVersionedRecipe fetches a recipe for a specific
// version. If the version matches the latest, uses the
// resolver directly. Otherwise falls back to the versioned
// registry index. Returns an error if the version can't be
// found.
func resolveVersionedRecipe(ctx *cmdContext, name, version string) (*recipe.Recipe, error) {
	return resolveRecipeForPin(
		name, version, ctx.Resolver, ctx.Registry,
	)
}

// reportResult prints the install/update result message.
func reportResult(out *output.Output, result *installer.InstallResult, verb, sourceLabel string) {
	switch result.Method {
	case installer.MethodCached:
		out.Success(fmt.Sprintf("%s %s@%s (already in store)",
			verb, result.Name, result.Version))
	case installer.MethodBinary:
		out.Success(fmt.Sprintf("%s %s@%s from binary",
			verb, result.Name, result.Version))
	case installer.MethodSource:
		out.Success(fmt.Sprintf("%s %s@%s (%s)",
			verb, result.Name, result.Version, sourceLabel))
	}
}

// FinalizeInstall adds a package to gale.toml, updates
// gale.lock, and rebuilds the generation. configVersion
// is written to gale.toml (bare by convention);
// lockVersion is the canonical `<version>-<revision>`
// written to gale.lock. ctx.Host controls section
// targeting (see WriteConfig).
//
// Uses lenient rebuild so unrelated missing store dirs in
// gale.toml don't block this install from landing on PATH —
// the common fresh-clone-on-new-host scenario (gh#23):
// gale.toml lists packages this host hasn't installed yet,
// strict rebuild errors on them, and the silent side-effect
// is "lockfile updated, store populated, gen never rotated,
// `which <pkg>` still resolves the prior revision." Sync
// already uses lenient for the same reason.
//
// Lenient on its own can mask the install actually failing
// to land — populateGeneration's `continue` on ErrNotExist
// could silently skip OUR package too if (e.g.) the store
// dir gets removed between Install and rebuild. The
// post-rebuild check below enforces the contract: this
// install must put the package on PATH, or surface a clear
// error.
func (ctx *cmdContext) FinalizeInstall(name, configVersion, lockVersion string) error {
	if err := ctx.WriteConfig(name, configVersion, lockVersion); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	if err := ctx.WriteLock(); err != nil {
		return fmt.Errorf("writing lock: %w", err)
	}
	if err := rebuildGeneration(ctx.GaleDir, ctx.StoreRoot, ctx.GalePath, nil); err != nil {
		return fmt.Errorf("rebuild generation: %w", err)
	}
	// --host targeting another machine is declaration-only:
	// the generation just rebuilt comes from the CURRENT
	// host's effective package set, so a package declared
	// for a foreign host is correctly absent from it.
	// Running the presence check anyway mis-reported every
	// cross-host install as store corruption — after config,
	// lock, and store were already mutated (gh#72).
	if ctx.Host != "" && !config.HostKeyMatches(ctx.Host, config.CurrentHost()) {
		return nil
	}
	active, err := generation.CurrentVersions(ctx.GaleDir, ctx.StoreRoot)
	if err != nil {
		return fmt.Errorf(
			"verify install landed on PATH: %w", err,
		)
	}
	if _, ok := active[name]; !ok {
		return fmt.Errorf(
			"%s@%s installed to store but did not land in the "+
				"active generation; the store dir may have been "+
				"removed mid-install",
			name, lockVersion,
		)
	}
	return nil
}

// FinalizeRecipeInstall is FinalizeInstall for the
// common case: pass a recipe and let the helper pick
// the canonical/bare forms. Bare goes to gale.toml so
// the entry tracks revision bumps automatically; the
// canonical `<v>-<N>` goes to gale.lock for exact pin.
// See configVersionForRecipe for the revision-rollback
// exception (gh#65).
func (ctx *cmdContext) FinalizeRecipeInstall(r *recipe.Recipe) error {
	return ctx.FinalizeInstall(
		r.Package.Name, configVersionForRecipe(ctx.StoreRoot, r),
		r.Package.Full(),
	)
}

// configVersionForRecipe returns the version form written to
// gale.toml for a recipe install. Bare by convention, so the
// entry tracks recipe revision bumps automatically. But when
// the append-only store already holds a different revision that
// the bare form would resolve to — the revision-rollback case,
// `gale switch pkg <v>-<rev>` or `gale install pkg@<v>-<rev>`
// with a higher revision on disk — a bare pin would activate
// that other revision and the requested one would silently
// never land on PATH (gh#65). Pin the canonical "<v>-<N>"
// instead so generation resolution matches it exactly.
func configVersionForRecipe(storeRoot string, r *recipe.Recipe) string {
	bare := r.Package.Version
	full := r.Package.Full()
	s := store.NewStore(storeRoot)
	bareDir, bareOK := s.StorePath(r.Package.Name, bare)
	fullDir, fullOK := s.StorePath(r.Package.Name, full)
	if bareOK && fullOK && bareDir != fullDir {
		return full
	}
	return bare
}

// WriteConfig adds a package to gale.toml and records the root for
// the lock write that follows. It rebuilds no generation and writes
// no lock: callers do both once per command, not once per package.
//
// configVersion is the form written to gale.toml (bare by
// convention so the entry tracks revision bumps automatically);
// lockVersion is the canonical `<version>-<revision>` form the lock
// roots, for exact pinning. See docs/revisions.md.
//
// ctx.Host selects where in gale.toml the package lands. When
// non-empty, the package is force-written to
// [hosts.<host>.packages]. When empty, the package is upserted with
// location preservation: if the current machine already lists it
// under its host overlay, that entry is updated in place; otherwise
// it goes to shared [packages]. The lock target follows whichever
// section was actually written, so a concrete-host operation never
// rewrites the shared graph and vice versa.
func (ctx *cmdContext) WriteConfig(name, configVersion, lockVersion string) error {
	if ctx.Host != "" {
		if err := config.AddPackage(
			ctx.GalePath, ctx.Host, name, configVersion,
		); err != nil {
			return fmt.Errorf("adding to config: %w", err)
		}
		ctx.noteLockRoot(ctx.Host, name, lockVersion)
		return nil
	}
	section, err := config.UpsertPackage(
		ctx.GalePath, config.CurrentHost(), name, configVersion,
	)
	if err != nil {
		return fmt.Errorf("adding to config: %w", err)
	}
	ctx.noteLockRoot(section, name, lockVersion)
	return nil
}

// WriteConfigForRecipe is WriteConfig for the recipe-in-hand case.
// See FinalizeRecipeInstall and configVersionForRecipe.
func (ctx *cmdContext) WriteConfigForRecipe(r *recipe.Recipe) error {
	return ctx.WriteConfig(
		r.Package.Name, configVersionForRecipe(ctx.StoreRoot, r),
		r.Package.Full(),
	)
}

// RebuildGeneration reads gale.toml and rebuilds the
// generation symlinks, including a timing phase for
// verbose output. Missing store dirs are silently skipped
// with a warning (see generation.Build).
func (ctx *cmdContext) RebuildGeneration() error {
	defer timing.Phase("generation-rebuild")()
	return rebuildGeneration(ctx.GaleDir, ctx.StoreRoot, ctx.GalePath, nil)
}

// RebuildGenerationCanonical is RebuildGeneration with
// bare config pins mapped to the recipe's canonical
// revision first, so the rebuild links the revision the
// recipe offers instead of a higher orphan left on disk
// by a withdrawn recipe revision (gh#137). Sync uses this;
// it costs a recipe resolution per bare pin.
func (ctx *cmdContext) RebuildGenerationCanonical() error {
	defer timing.Phase("generation-rebuild")()
	return rebuildGeneration(
		ctx.GaleDir, ctx.StoreRoot, ctx.GalePath,
		ctx.versionedRecipeResolver(),
	)
}

// ResolveVersionedRecipe fetches a recipe for a specific
// version.
func (ctx *cmdContext) ResolveVersionedRecipe(name, version string) (*recipe.Recipe, error) {
	return resolveVersionedRecipe(ctx, name, version)
}
