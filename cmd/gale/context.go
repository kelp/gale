package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/kelp/gale/internal/attestation"
	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/registry"
	"github.com/kelp/gale/internal/store"
)

// cmdContext holds resolved config, store, and installer
// shared by sync, update, and install commands.
type cmdContext struct {
	GalePath  string // path to gale.toml
	GaleDir   string // .gale directory (project or global)
	StoreRoot string
	Resolver  installer.RecipeResolver
	Installer *installer.Installer
	Registry  *registry.Registry // nil when --local
}

// newCmdContext resolves the config, store, and installer.
// If local is true, recipes are resolved from a sibling
// gale-recipes directory.
func newCmdContext(local bool) (*cmdContext, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("getting working dir: %w", err)
	}

	// Find config: project gale.toml first, then global.
	galePath, err := config.FindGaleConfig(cwd)
	if err != nil {
		globalDir, dirErr := galeConfigDir()
		if dirErr != nil {
			return nil, dirErr
		}
		galePath = filepath.Join(globalDir, "gale.toml")
	}

	// Resolve galeDir: project .gale/ or global ~/.gale/.
	galeDir, err := galeConfigDir()
	if err != nil {
		return nil, err
	}
	configDir := filepath.Dir(galePath)
	globalDir, _ := galeConfigDir()
	if configDir != globalDir {
		galeDir = filepath.Join(configDir, ".gale")
	}

	// Set up resolver.
	storeRoot := defaultStoreRoot()
	var resolver installer.RecipeResolver
	var reg *registry.Registry
	if local {
		recipesDir, dirErr := findLocalRecipesDir(cwd)
		if dirErr != nil {
			return nil, dirErr
		}
		resolver = localRecipeResolver(recipesDir)
	} else {
		reg = newRegistry()
		resolver = reg.FetchRecipe
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
		if os.IsNotExist(err) {
			return ctx.loadToolVersionsFallback()
		}
		return nil, fmt.Errorf("reading %s: %w", ctx.GalePath, err)
	}
	return config.ParseGaleConfig(string(data))
}

// loadToolVersionsFallback checks for a .tool-versions file
// in the same directory as the expected gale.toml.
func (ctx *cmdContext) loadToolVersionsFallback() (*config.GaleConfig, error) {
	dir := filepath.Dir(ctx.GalePath)
	tvPath := filepath.Join(dir, ".tool-versions")
	data, err := os.ReadFile(tvPath)
	if err != nil {
		if os.IsNotExist(err) {
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

// rebuildGeneration reads gale.toml and rebuilds the
// generation symlinks.
func rebuildGeneration(galeDir, storeRoot, configPath string) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			// No config yet — build empty generation.
			return generation.Build(
				map[string]string{}, galeDir, storeRoot)
		}
		return fmt.Errorf("read config: %w", err)
	}

	cfg, err := config.ParseGaleConfig(string(data))
	if err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	pkgs := cfg.Packages
	if pkgs == nil {
		pkgs = map[string]string{}
	}

	return generation.Build(pkgs, galeDir, storeRoot)
}

// loadAppConfig reads and parses ~/.gale/config.toml.
func loadAppConfig() (*config.AppConfig, error) {
	galeDir, err := galeConfigDir()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(galeDir, "config.toml"))
	if err != nil {
		return nil, err
	}
	return config.ParseAppConfig(string(data))
}

// newRegistry creates a Registry, using the URL from
// ~/.gale/config.toml if configured.
func newRegistry() *registry.Registry {
	cfg, err := loadAppConfig()
	if err != nil {
		return registry.New()
	}
	return registry.NewWithURL(cfg.Registry.URL)
}

// lockfilePath returns the gale.lock path for a given
// gale.toml path.
func lockfilePath(configPath string) string {
	return configPath[:len(configPath)-len(".toml")] + ".lock"
}

// writeConfigAndLock adds a package to gale.toml and
// updates gale.lock. Does not rebuild the generation —
// callers handle that (once per command, not per package).
func writeConfigAndLock(configPath, name, version, sha256 string) error {
	if err := config.AddPackage(configPath, name, version); err != nil {
		return fmt.Errorf("adding to config: %w", err)
	}
	return updateLockfile(
		lockfilePath(configPath), name, version, sha256)
}

// finalizeInstall adds a package to gale.toml, updates
// gale.lock, and rebuilds the generation.
func finalizeInstall(galeDir, storeRoot, configPath, name, version, sha256 string) error {
	if err := writeConfigAndLock(
		configPath, name, version, sha256); err != nil {
		return err
	}
	return rebuildGeneration(galeDir, storeRoot, configPath)
}

// updateLockfile reads the lockfile, updates one package
// entry, and writes it back.
func updateLockfile(lockPath, name, version, sha256 string) error {
	lf, err := lockfile.Read(lockPath)
	if err != nil {
		return err
	}
	lf.Packages[name] = lockfile.LockedPackage{
		Version: version,
		SHA256:  sha256,
	}
	return lockfile.Write(lockPath, lf)
}

// addToConfig resolves scope and writes a package version
// to gale.toml. Returns the config path used.
func addToConfig(name, version string, global, project bool) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getting working dir: %w", err)
	}
	useGlobal := resolveScope(global, project,
		cwd, isStdinTTY(), os.Stdin)
	configPath, err := resolveConfigPath(useGlobal)
	if err != nil {
		return "", err
	}
	if err := config.AddPackage(configPath, name, version); err != nil {
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
	r, err := ctx.Resolver(name)
	if err == nil && r.Package.Version == version {
		return r, nil
	}

	// Try versioned registry fetch (not available in
	// --local mode).
	if ctx.Registry != nil {
		pinned, vErr := ctx.Registry.FetchRecipeVersion(
			name, version)
		if vErr == nil {
			return pinned, nil
		}
	}

	if err != nil {
		return nil, fmt.Errorf(
			"resolving %s@%s: %w", name, version, err)
	}
	return nil, fmt.Errorf(
		"%s@%s not found (registry has %s)",
		name, version, r.Package.Version)
}

// reportResult prints the install/update result message.
func reportResult(out *output.Output, result *installer.InstallResult, verb, sourceLabel string) {
	switch result.Method {
	case "cached":
		out.Info(fmt.Sprintf("%s@%s already installed",
			result.Name, result.Version))
	case "binary":
		out.Success(fmt.Sprintf("%s %s@%s from binary",
			verb, result.Name, result.Version))
	case "source":
		out.Success(fmt.Sprintf("%s %s@%s (%s)",
			verb, result.Name, result.Version, sourceLabel))
	}
}
