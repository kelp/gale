package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kelp/gale/internal/attestation"
	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/filelock"
	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/output"
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
					"no project found — run 'gale init' first")
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

	inst := &installer.Installer{
		Store:    store.NewStore(storeRoot),
		Resolver: resolver,
		Verifier: attestation.NewVerifier(),
	}

	return &cmdContext{
		GalePath:  galePath,
		GaleDir:   galeDir,
		StoreRoot: storeRoot,
		Resolver:  resolver,
		Installer: inst,
		Registry:  reg,
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
// config and rebuilds the generation symlinks. Fails if a
// referenced store dir is missing — callers that need to
// tolerate partial install failures (only sync today)
// should use rebuildGenerationLenient instead.
func rebuildGeneration(galeDir, storeRoot, configPath string) error {
	pkgs, err := readConfigPackages(configPath)
	if err != nil {
		return err
	}
	return generation.Build(pkgs, galeDir, storeRoot)
}

// rebuildGenerationLenient is rebuildGeneration but
// silently skips packages whose store dir is missing.
// Sync uses this so a batch where one install failed
// still lands the successful installs on PATH — per
// Issue #20. The install failure is surfaced separately.
func rebuildGenerationLenient(galeDir, storeRoot, configPath string) error {
	pkgs, err := readConfigPackages(configPath)
	if err != nil {
		return err
	}
	return generation.BuildLenient(pkgs, galeDir, storeRoot)
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
func writeConfigAndLock(configPath, host, name, configVersion, lockVersion, sha256 string) error {
	if host != "" {
		if err := config.AddPackage(
			configPath, host, name, configVersion); err != nil {
			return fmt.Errorf("adding to config: %w", err)
		}
	} else if err := config.UpsertPackage(
		configPath, config.CurrentHost(), name, configVersion); err != nil {
		return fmt.Errorf("adding to config: %w", err)
	}
	lp, err := lockfilePath(configPath)
	if err != nil {
		return fmt.Errorf("resolving lockfile path: %w", err)
	}
	if sha256 == "" {
		// Cached install: preserve existing hash only if
		// the lockfile version matches.
		lf, err := lockfile.Read(lp)
		if err != nil {
			return fmt.Errorf("reading lockfile: %w", err)
		}
		if existing, ok := lf.Packages[name]; ok &&
			lockfile.VersionMatches(lockVersion, existing.Version) {
			return nil // same version, keep existing hash
		}
	}
	return updateLockfile(lp, name, lockVersion, sha256)
}

// finalizeInstall adds a package to gale.toml, updates
// gale.lock, and rebuilds the generation. See
// writeConfigAndLock for host semantics.
func finalizeInstall(galeDir, storeRoot, configPath, host, name, configVersion, lockVersion, sha256 string) error {
	if err := writeConfigAndLock(
		configPath, host, name, configVersion, lockVersion, sha256); err != nil {
		return fmt.Errorf("writing config and lock: %w", err)
	}
	if err := rebuildGeneration(galeDir, storeRoot, configPath); err != nil {
		return fmt.Errorf("rebuild generation: %w", err)
	}
	return nil
}

// updateLockfile reads the lockfile, updates one package
// entry, and writes it back. The file lock serializes
// concurrent read-modify-write operations.
func updateLockfile(lockPath, name, version, sha256 string) error {
	defer timing.Phase("lockfile-write " + name)()
	return filelock.With(lockPath+".lock", func() error {
		lf, err := lockfile.Read(lockPath)
		if err != nil {
			return fmt.Errorf("reading lockfile: %w", err)
		}
		lf.Packages[name] = lockfile.LockedPackage{
			Version: version,
			SHA256:  sha256,
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
// tags like "1.0.0-rc1" are left unchanged.
func stripNumericRevision(version string) string {
	if i := strings.LastIndex(version, "-"); i >= 0 {
		suffix := version[i+1:]
		if len(suffix) > 0 && strings.IndexFunc(suffix, func(r rune) bool {
			return r < '0' || r > '9'
		}) == -1 {
			return version[:i]
		}
	}
	return version
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
			configPath, host, name, version); err != nil {
			return "", fmt.Errorf("adding %s to config: %w", name, err)
		}
		return configPath, nil
	}
	if err := config.UpsertPackage(
		configPath, config.CurrentHost(), name, version); err != nil {
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
			name, version)
		if vErr == nil {
			return pinned, nil
		}
	}

	if err != nil {
		return nil, fmt.Errorf(
			"resolving %s@%s: %w", name, version, err)
	}
	if vErr != nil {
		return nil, fmt.Errorf(
			"%s@%s not found (registry has %s): %w",
			name, version, r.Package.Version, vErr)
	}
	return nil, fmt.Errorf(
		"%s@%s not found (registry has %s)",
		name, version, r.Package.Version)
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
func (ctx *cmdContext) FinalizeInstall(name, configVersion, lockVersion, sha256 string) error {
	return finalizeInstall(
		ctx.GaleDir, ctx.StoreRoot, ctx.GalePath, ctx.Host,
		name, configVersion, lockVersion, sha256)
}

// FinalizeRecipeInstall is FinalizeInstall for the
// common case: pass a recipe and let the helper pick
// the canonical/bare forms. Bare goes to gale.toml so
// the entry tracks revision bumps automatically; the
// canonical `<v>-<N>` goes to gale.lock for exact pin.
func (ctx *cmdContext) FinalizeRecipeInstall(r *recipe.Recipe, sha256 string) error {
	return ctx.FinalizeInstall(
		r.Package.Name, r.Package.Version, r.Package.Full(), sha256)
}

// WriteConfigAndLock adds a package to gale.toml and
// updates gale.lock without rebuilding the generation.
// ctx.Host controls section targeting (see
// writeConfigAndLock).
func (ctx *cmdContext) WriteConfigAndLock(name, configVersion, lockVersion, sha256 string) error {
	return writeConfigAndLock(
		ctx.GalePath, ctx.Host, name, configVersion, lockVersion, sha256)
}

// WriteConfigAndLockForRecipe is WriteConfigAndLock for
// the recipe-in-hand case. See FinalizeRecipeInstall.
func (ctx *cmdContext) WriteConfigAndLockForRecipe(r *recipe.Recipe, sha256 string) error {
	return ctx.WriteConfigAndLock(
		r.Package.Name, r.Package.Version, r.Package.Full(), sha256)
}

// RebuildGeneration reads gale.toml and rebuilds the
// generation symlinks.
func (ctx *cmdContext) RebuildGeneration() error {
	return rebuildGeneration(ctx.GaleDir, ctx.StoreRoot, ctx.GalePath)
}

// RebuildGenerationLenient is RebuildGeneration but
// tolerates missing store dirs (see
// rebuildGenerationLenient). Sync uses this.
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
