package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kelp/gale/internal/attestation"
	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/filelock"
	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/parallel"
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
	// preserves the existing location (see writeConfigAndLock).
	Host string
}

// newCmdContext resolves the config, store, and installer.
// When recipesPath is non-empty, recipes are resolved locally:
// "auto" uses sibling gale-recipes/ detection, any other value
// is used as an explicit path.
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
				return nil, fmt.Errorf(
					"no project found — run 'gale init' first",
				)
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

	// Set up resolver.
	storeRoot := defaultStoreRoot()
	resolver, reg, resolveErr := resolveRecipeResolver(recipesPath, cwd)
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

// LoadConfig reads and parses the gale.toml that this
// context points to. If gale.toml doesn't exist, falls
// back to reading .tool-versions in the same directory.
func (ctx *cmdContext) LoadConfig() (*config.GaleConfig, error) {
	data, err := os.ReadFile(ctx.GalePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ctx.loadToolVersionsFallback()
		}
		return nil, fmt.Errorf("reading %s: %w", ctx.GalePath, err)
	}
	cfg, err := config.ParseGaleConfig(string(data))
	if err != nil {
		return nil, err
	}
	cfg.ApplyHost(config.CurrentHost())
	return cfg, nil
}

// loadToolVersionsFallback checks for a .tool-versions file
// in the same directory as the expected gale.toml.
func (ctx *cmdContext) loadToolVersionsFallback() (*config.GaleConfig, error) {
	dir := filepath.Dir(ctx.GalePath)
	tvPath := filepath.Join(dir, ".tool-versions")
	data, err := os.ReadFile(tvPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &config.GaleConfig{
				Packages: map[string]string{},
			}, nil
		}
		return nil, fmt.Errorf("reading .tool-versions: %w", err)
	}

	pkgs, err := config.ParseToolVersions(string(data))
	if err != nil {
		return nil, fmt.Errorf("parsing .tool-versions: %w", err)
	}
	return &config.GaleConfig{Packages: pkgs}, nil
}

// rebuildGeneration reads the effective project/global
// config and rebuilds the generation symlinks. Tolerates
// packages whose store dir is missing (skip-with-warning):
// gale.toml legitimately lists packages that aren't
// installed locally — `gale add` without sync, a fresh
// clone on a new host, an unsupported-platform skip — and
// the previous strict rebuild let update/remove commit
// their config+lock mutations while the generation never
// rotated, desyncing PATH from config (gh#68). The skipped
// package is reported on stderr by BuildLenient.
func rebuildGeneration(galeDir, storeRoot, configPath string) error {
	return rebuildGenerationLenient(galeDir, storeRoot, configPath)
}

// rebuildGenerationLenient rebuilds the generation,
// skipping (with a warning) packages whose store dir is
// missing. Sync uses this so a batch where one install
// failed still lands the successful installs on PATH — per
// Issue #20. The install failure is surfaced separately.
func rebuildGenerationLenient(galeDir, storeRoot, configPath string) error {
	pkgs, err := readConfigPackages(configPath)
	if err != nil {
		return err
	}
	if err := generation.BuildLenient(pkgs, galeDir, storeRoot); err != nil {
		return err
	}
	autoPruneGenerations(galeDir, storeRoot)
	return nil
}

// autoPruneGenerations is the post-Build hook that bounds gen
// dir accumulation. Every command that creates a generation
// (install, update, sync, add, remove, recipe install, ...)
// routes through rebuildGeneration / rebuildGenerationLenient,
// so a single hook here covers all of them. Reads the keep
// count from ~/.gale/config.toml [generation] keep, defaults
// to DefaultGenerationKeep when unset, treats negative as
// "disabled."
//
// Errors during prune are surfaced as warnings, not failures —
// the install / sync that triggered this already succeeded and
// must not regress because of a cleanup hiccup. Removed gen
// numbers are reported on stderr so users see what happened.
func autoPruneGenerations(galeDir, storeRoot string) {
	keep := loadGenerationKeep(galeDir)
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
// The config path is the global one regardless of galeDir
// (which may be project-scoped) since gen retention is an
// app-level concern, not per-project.
func loadGenerationKeep(_ string) int {
	dir, err := galeConfigDir()
	if err != nil {
		return config.DefaultGenerationKeep
	}
	data, err := os.ReadFile(filepath.Join(dir, "config.toml"))
	if err != nil {
		return config.DefaultGenerationKeep
	}
	cfg, err := config.ParseAppConfig(string(data))
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
	data, err := os.ReadFile(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			dir := filepath.Dir(configPath)
			tvPath := filepath.Join(dir, ".tool-versions")
			data, err := os.ReadFile(tvPath)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return &config.GaleConfig{Packages: map[string]string{}}, nil
				}
				return nil, fmt.Errorf("reading .tool-versions: %w", err)
			}
			pkgs, err := config.ParseToolVersions(string(data))
			if err != nil {
				return nil, fmt.Errorf("parsing .tool-versions: %w", err)
			}
			return &config.GaleConfig{Packages: pkgs}, nil
		}
		return nil, fmt.Errorf("reading config: %w", err)
	}

	cfg, err := config.ParseGaleConfig(string(data))
	if err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
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

// writeConfigAndLock adds a package to gale.toml and
// updates gale.lock. Does not rebuild the generation —
// callers handle that (once per command, not per package).
// When sha256 is empty (cached install), the lockfile
// entry is still updated with the new version so stale
// hashes from a previous version are not retained.
//
// configVersion is the form written to gale.toml (bare
// by convention so the entry tracks revision bumps
// automatically); lockVersion is the canonical
// `<version>-<revision>` form written to gale.lock for
// exact pinning. See docs/revisions.md.
//
// host selects where in gale.toml the package lands.
// When non-empty, the package is force-written to
// [hosts.<host>.packages]. When empty, the package is
// upserted with location preservation: if the current
// machine already lists it under its host overlay, that
// entry is updated in place; otherwise it goes to shared
// [packages].
func writeConfigAndLock(configPath, host, name, configVersion, lockVersion, sha256, manifestDigest string) error {
	if host != "" {
		if err := config.AddPackage(
			configPath, host, name, configVersion,
		); err != nil {
			return fmt.Errorf("adding to config: %w", err)
		}
	} else if err := config.UpsertPackage(
		configPath, config.CurrentHost(), name, configVersion,
	); err != nil {
		return fmt.Errorf("adding to config: %w", err)
	}
	lp, err := lockfilePath(configPath)
	if err != nil {
		return fmt.Errorf("resolving lockfile path: %w", err)
	}
	if sha256 == "" {
		// Cached install: no new hash to record. Preserve the
		// existing entry only when the stored version is
		// EXACTLY the canonical lockVersion. A loose
		// VersionMatches early-return drops the revision
		// (gh#30): a bare "2.53.0" in the lock satisfies
		// VersionMatches against the resolved "2.53.0-2", so
		// the canonical form was never written and the bare
		// pin stuck. Rewrite to lockVersion, carrying the old
		// hash forward since none was computed this run.
		lf, err := lockfile.Read(lp)
		if err != nil {
			return fmt.Errorf("reading lockfile: %w", err)
		}
		if existing, ok := lf.Packages[name]; ok {
			if existing.Version == lockVersion {
				return nil // identical pin, keep existing hash
			}
			if lockfile.VersionMatches(lockVersion, existing.Version) {
				// Same upstream version, differing revision
				// representation (bare vs canonical). Rewrite to
				// the canonical lockVersion but keep the hash and
				// manifest digest.
				return updateLockfile(
					lp, name, lockVersion,
					existing.SHA256, existing.ManifestDigest,
				)
			}
		}
	}
	return updateLockfile(lp, name, lockVersion, sha256, manifestDigest)
}

// finalizeInstall adds a package to gale.toml, updates
// gale.lock, and rebuilds the generation. See
// writeConfigAndLock for host semantics.
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
func finalizeInstall(galeDir, storeRoot, configPath, host, name, configVersion, lockVersion, sha256, manifestDigest string) error {
	if err := writeConfigAndLock(
		configPath, host, name, configVersion, lockVersion,
		sha256, manifestDigest,
	); err != nil {
		return fmt.Errorf("writing config and lock: %w", err)
	}
	if err := rebuildGenerationLenient(galeDir, storeRoot, configPath); err != nil {
		return fmt.Errorf("rebuild generation: %w", err)
	}
	// --host targeting another machine is declaration-only:
	// the generation just rebuilt comes from the CURRENT
	// host's effective package set, so a package declared
	// for a foreign host is correctly absent from it.
	// Running the presence check anyway mis-reported every
	// cross-host install as store corruption — after config,
	// lock, and store were already mutated (gh#72).
	if host != "" && !config.HostKeyMatches(host, config.CurrentHost()) {
		return nil
	}
	active, err := generation.CurrentVersions(galeDir, storeRoot)
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

// updateLockfile reads the lockfile, updates one package
// entry, and writes it back. The file lock serializes
// concurrent read-modify-write operations.
//
// manifestDigest is the OCI manifest digest to persist
// alongside the hash; empty for source builds.
func updateLockfile(lockPath, name, version, sha256, manifestDigest string) error {
	defer timing.Phase("lockfile-write " + name)()
	return filelock.With(lockPath+".lock", func() error {
		lf, err := lockfile.Read(lockPath)
		if err != nil {
			return fmt.Errorf("reading lockfile: %w", err)
		}
		lf.Packages[name] = lockfile.LockedPackage{
			Version:        version,
			SHA256:         sha256,
			ManifestDigest: manifestDigest,
		}
		return lockfile.Write(lockPath, lf)
	})
}

// removeLockEntry removes a package entry from the lockfile.
func removeLockEntry(configPath, name string) error {
	lp, err := lockfilePath(configPath)
	if err != nil {
		return fmt.Errorf("resolving lockfile path: %w", err)
	}
	return filelock.With(lp+".lock", func() error {
		lf, err := lockfile.Read(lp)
		if err != nil {
			return fmt.Errorf("reading lockfile: %w", err)
		}
		delete(lf.Packages, name)
		return lockfile.Write(lp, lf)
	})
}

// stripNumericRevision removes a Debian-style "-N" suffix from
// a version string when N is all decimal digits. Pre-release
// tags like "1.0.0-rc1" are left unchanged. Delegates to the
// canonical revision parser in internal/store.
func stripNumericRevision(version string) string {
	base, _ := store.SplitRevision(version)
	return base
}

// addToConfig resolves scope and writes a package version to
// gale.toml. Returns the config path used. When host is
// non-empty, writes to [hosts.<host>.packages]; otherwise
// writes to the shared [packages] section, preserving an
// existing host-scoped entry for the current machine.
func addToConfig(name, version, host string, global, project bool) (string, error) {
	version = stripNumericRevision(version)
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getting working dir: %w", err)
	}
	useGlobal := resolveScope(global, project, cwd)
	configPath, err := resolveConfigPath(useGlobal)
	if err != nil {
		return "", fmt.Errorf("resolving config path: %w", err)
	}
	if host != "" {
		if err := config.AddPackage(
			configPath, host, name, version,
		); err != nil {
			return "", fmt.Errorf("adding %s to config: %w", name, err)
		}
		return configPath, nil
	}
	if err := config.UpsertPackage(
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
	// Try the resolver first — if latest matches, use it.
	// Compare both bare Version and Full() (with revision) so
	// a request for "1.2.3-2" matches a recipe whose Version is
	// "1.2.3" and Revision is 2.
	r, err := ctx.Resolver(name)
	if err == nil &&
		(r.Package.Version == version || r.Package.Full() == version) {
		return r, nil
	}

	// Try versioned registry fetch (not available in
	// --recipes mode).
	var vErr error
	if ctx.Registry != nil {
		var pinned *recipe.Recipe
		pinned, vErr = ctx.Registry.FetchRecipeVersion(
			name, version,
		)
		if vErr == nil {
			return pinned, nil
		}
	}

	if err != nil {
		return nil, fmt.Errorf(
			"resolving %s@%s: %w", name, version, err,
		)
	}
	if vErr != nil {
		return nil, fmt.Errorf(
			"%s@%s not found (registry has %s): %w",
			name, version, r.Package.Version, vErr,
		)
	}
	return nil, fmt.Errorf(
		"%s@%s not found (registry has %s)",
		name, version, r.Package.Version,
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
// targeting (see writeConfigAndLock).
func (ctx *cmdContext) FinalizeInstall(name, configVersion, lockVersion, sha256, manifestDigest string) error {
	return finalizeInstall(
		ctx.GaleDir, ctx.StoreRoot, ctx.GalePath, ctx.Host,
		name, configVersion, lockVersion, sha256, manifestDigest,
	)
}

// FinalizeRecipeInstall is FinalizeInstall for the
// common case: pass a recipe and let the helper pick
// the canonical/bare forms. Bare goes to gale.toml so
// the entry tracks revision bumps automatically; the
// canonical `<v>-<N>` goes to gale.lock for exact pin.
// See configVersionForRecipe for the revision-rollback
// exception (gh#65).
func (ctx *cmdContext) FinalizeRecipeInstall(r *recipe.Recipe, sha256, manifestDigest string) error {
	return ctx.FinalizeInstall(
		r.Package.Name, configVersionForRecipe(ctx.StoreRoot, r),
		r.Package.Full(), sha256, manifestDigest,
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

// WriteConfigAndLock adds a package to gale.toml and
// updates gale.lock without rebuilding the generation.
// ctx.Host controls section targeting (see
// writeConfigAndLock).
func (ctx *cmdContext) WriteConfigAndLock(name, configVersion, lockVersion, sha256, manifestDigest string) error {
	return writeConfigAndLock(
		ctx.GalePath, ctx.Host, name, configVersion, lockVersion,
		sha256, manifestDigest,
	)
}

// WriteConfigAndLockForRecipe is WriteConfigAndLock for
// the recipe-in-hand case. See FinalizeRecipeInstall and
// configVersionForRecipe.
func (ctx *cmdContext) WriteConfigAndLockForRecipe(r *recipe.Recipe, sha256, manifestDigest string) error {
	return ctx.WriteConfigAndLock(
		r.Package.Name, configVersionForRecipe(ctx.StoreRoot, r),
		r.Package.Full(), sha256, manifestDigest,
	)
}

// RebuildGeneration reads gale.toml and rebuilds the
// generation symlinks.
func (ctx *cmdContext) RebuildGeneration() error {
	return rebuildGeneration(ctx.GaleDir, ctx.StoreRoot, ctx.GalePath)
}

// RebuildGenerationLenient is RebuildGeneration plus a
// timing phase (see rebuildGenerationLenient). Both
// tolerate missing store dirs since gh#68. Sync uses this.
func (ctx *cmdContext) RebuildGenerationLenient() error {
	defer timing.Phase("generation-rebuild")()
	return rebuildGenerationLenient(ctx.GaleDir, ctx.StoreRoot, ctx.GalePath)
}

// RemoveLockEntry removes a package entry from the lockfile.
func (ctx *cmdContext) RemoveLockEntry(name string) error {
	return removeLockEntry(ctx.GalePath, name)
}

// ResolveVersionedRecipe fetches a recipe for a specific
// version.
func (ctx *cmdContext) ResolveVersionedRecipe(name, version string) (*recipe.Recipe, error) {
	return resolveVersionedRecipe(ctx, name, version)
}
