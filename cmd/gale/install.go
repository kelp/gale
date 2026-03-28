package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/registry"
	"github.com/kelp/gale/internal/store"
	"github.com/spf13/cobra"
)

var (
	installGlobal  bool
	installProject bool
	installRecipe  string
	installSource  string
	installGit     bool
)

var installCmd = &cobra.Command{
	Use:   "install <package>[@version]",
	Short: "Install a package",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateInstallFlags(installGlobal, installProject); err != nil {
			return err
		}

		name, version := parsePackageArg(args[0])
		out := output.New(os.Stderr, !cmd.Flags().Changed("no-color"))

		// If --source flag is provided, build from local source.
		if installSource != "" {
			return installFromLocalSource(name, installRecipe, installSource, out)
		}

		// If --git flag is provided, clone and build from git.
		if installGit {
			return installFromGit(name, installRecipe, out)
		}

		// If --recipe flag is provided, install from recipe file.
		if installRecipe != "" {
			return installFromRecipeFile(installRecipe, out)
		}

		// Fetch recipe from registry.
		reg := newRegistry()
		out.Info(fmt.Sprintf("Fetching recipe for %s...", name))

		r, err := reg.FetchRecipe(name)
		if err != nil {
			return fmt.Errorf("fetching recipe: %w", err)
		}

		// Check version match if a specific version was requested.
		if err := checkVersionMatch(version, r.Package.Version); err != nil {
			return err
		}

		// Set up installer.
		galeDir, err := galeConfigDir()
		if err != nil {
			return err
		}

		storeRoot := defaultStoreRoot()

		inst := &installer.Installer{
			Store:    store.NewStore(storeRoot),
			Resolver: reg.FetchRecipe,
		}

		out.Info(fmt.Sprintf("Installing %s@%s...",
			r.Package.Name, r.Package.Version))

		result, err := inst.Install(r)
		if err != nil {
			return fmt.Errorf("install failed: %w", err)
		}

		// Determine scope and add to gale.toml.
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working dir: %w", err)
		}
		useGlobal := resolveScope(installGlobal, installProject,
			cwd, isStdinTTY(), os.Stdin)
		configPath, err := resolveConfigPath(useGlobal)
		if err != nil {
			return err
		}

		if err := config.AddPackage(configPath, name,
			r.Package.Version); err != nil {
			return fmt.Errorf("adding to config: %w", err)
		}

		// Rebuild generation from gale.toml.
		if err := rebuildGeneration(galeDir, storeRoot,
			configPath); err != nil {
			return fmt.Errorf("rebuild generation: %w", err)
		}

		switch result.Method {
		case "cached":
			out.Success(fmt.Sprintf("%s@%s already installed",
				result.Name, result.Version))
		case "binary":
			out.Success(fmt.Sprintf("Installed %s@%s from binary",
				result.Name, result.Version))
		case "source":
			out.Success(fmt.Sprintf(
				"Installed %s@%s (built from source)",
				result.Name, result.Version))
		}

		return nil
	},
}

func init() {
	installCmd.Flags().BoolVarP(&installGlobal, "global", "g",
		false, "Install to global config")
	installCmd.Flags().BoolVarP(&installProject, "project", "p",
		false, "Install to project config")
	installCmd.Flags().StringVar(&installRecipe, "recipe", "",
		"Install from a recipe TOML file")
	installCmd.Flags().StringVar(&installSource, "source", "",
		"Build from a local source directory")
	installCmd.Flags().BoolVar(&installGit, "git", false,
		"Clone and build from git repository")
	rootCmd.AddCommand(installCmd)
}

// checkVersionMatch returns an error if the requested version
// doesn't match the recipe version. Empty or "latest" always
// matches.
func checkVersionMatch(requested, actual string) error {
	if requested == "" || requested == "latest" {
		return nil
	}
	if requested != actual {
		return fmt.Errorf(
			"version mismatch: requested %s but recipe has %s",
			requested, actual)
	}
	return nil
}

// validateInstallFlags returns an error if conflicting flags
// are set.
func validateInstallFlags(global, project bool) error {
	if global && project {
		return fmt.Errorf(
			"cannot use both --global and --project")
	}
	return nil
}

// resolveScope determines whether to use global config.
// Returns true for global, false for project. When no flag is
// set and a project gale.toml exists, prompts via reader if
// isTTY is true, otherwise defaults to global.
func resolveScope(global, project bool, cwd string, isTTY bool, reader io.Reader) bool {
	if global {
		return true
	}
	if project {
		return false
	}

	// Check if a project gale.toml exists.
	_, err := config.FindGaleConfig(cwd)
	if err != nil {
		// No project config found — use global.
		return true
	}

	// Project config exists. Prompt if TTY.
	if !isTTY {
		return true
	}

	fmt.Fprintf(os.Stderr,
		"gale.toml found — install globally or to project? [g/p] ")
	scanner := bufio.NewScanner(reader)
	if scanner.Scan() {
		answer := strings.TrimSpace(strings.ToLower(scanner.Text()))
		if answer == "p" {
			return false
		}
	}
	return true
}

// isStdinTTY returns true if stdin is a terminal.
func isStdinTTY() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// newRegistry creates a Registry, using the URL from
// ~/.gale/config.toml if configured.
func newRegistry() *registry.Registry {
	galeDir, err := galeConfigDir()
	if err != nil {
		return registry.New()
	}

	data, err := os.ReadFile(
		filepath.Join(galeDir, "config.toml"))
	if err != nil {
		return registry.New()
	}

	cfg, err := config.ParseAppConfig(string(data))
	if err != nil {
		return registry.New()
	}

	return registry.NewWithURL(cfg.Registry.URL)
}

func installFromGit(name, recipePath string, out *output.Output) error {
	// Resolve recipe from registry or --recipe flag.
	var r *recipe.Recipe
	var resolvedRecipe string
	if recipePath != "" {
		resolvedRecipe = recipePath
	} else {
		// Fetch from registry to get the recipe.
		reg := newRegistry()
		fetched, err := reg.FetchRecipe(name)
		if err != nil {
			return fmt.Errorf("fetching recipe: %w", err)
		}
		r = fetched
	}

	if resolvedRecipe != "" {
		data, err := os.ReadFile(resolvedRecipe)
		if err != nil {
			return fmt.Errorf("reading recipe: %w", err)
		}
		parsed, err := recipe.ParseLocal(string(data))
		if err != nil {
			return fmt.Errorf("parsing recipe: %w", err)
		}
		r = parsed
	}

	if r.Source.Repo == "" {
		return fmt.Errorf(
			"recipe for %s has no source.repo — cannot build from git", name)
	}

	galeDir, err := galeConfigDir()
	if err != nil {
		return err
	}

	storeRoot := defaultStoreRoot()
	inst := &installer.Installer{
		Store:    store.NewStore(storeRoot),
		Resolver: newRegistry().FetchRecipe,
	}

	out.Info(fmt.Sprintf("Installing %s from git (%s)...",
		r.Package.Name, r.Source.Repo))

	result, err := inst.InstallGit(r)
	if err != nil {
		return fmt.Errorf("install failed: %w", err)
	}

	// Add to global gale.toml and rebuild generation.
	configPath := filepath.Join(galeDir, "gale.toml")
	if err := config.AddPackage(configPath,
		r.Package.Name, result.Version); err != nil {
		return fmt.Errorf("adding to config: %w", err)
	}
	if err := rebuildGeneration(galeDir, storeRoot,
		configPath); err != nil {
		return fmt.Errorf("rebuild generation: %w", err)
	}

	switch result.Method {
	case "cached":
		out.Success(fmt.Sprintf("%s@%s already installed",
			result.Name, result.Version))
	case "source":
		out.Success(fmt.Sprintf(
			"Installed %s@%s (built from git)",
			result.Name, result.Version))
	}

	return nil
}

func installFromLocalSource(name, recipePath, sourceDir string, out *output.Output) error {
	// Resolve source directory to absolute path.
	absSource, err := filepath.Abs(sourceDir)
	if err != nil {
		return fmt.Errorf("resolve source path: %w", err)
	}

	// Resolve the recipe file.
	resolvedRecipe, err := resolveRecipePath(name, recipePath, absSource)
	if err != nil {
		return err
	}

	data, err := os.ReadFile(resolvedRecipe)
	if err != nil {
		return fmt.Errorf("reading recipe: %w", err)
	}

	r, err := recipe.ParseLocal(string(data))
	if err != nil {
		return fmt.Errorf("parsing recipe: %w", err)
	}

	// Override version with git short hash from source dir.
	version, err := gitShortHash(absSource)
	if err != nil {
		return fmt.Errorf("detecting version: %w", err)
	}
	r.Package.Version = version

	galeDir, err := galeConfigDir()
	if err != nil {
		return err
	}

	storeRoot := defaultStoreRoot()

	inst := &installer.Installer{
		Store:    store.NewStore(storeRoot),
		Resolver: recipeFileResolver(resolvedRecipe),
	}

	out.Info(fmt.Sprintf("Installing %s@%s from local source...",
		r.Package.Name, r.Package.Version))

	result, err := inst.InstallLocal(r, absSource)
	if err != nil {
		return fmt.Errorf("install failed: %w", err)
	}

	// Add to global gale.toml and rebuild generation.
	configPath := filepath.Join(galeDir, "gale.toml")
	if err := config.AddPackage(configPath,
		r.Package.Name, r.Package.Version); err != nil {
		return fmt.Errorf("adding to config: %w", err)
	}
	if err := rebuildGeneration(galeDir, storeRoot,
		configPath); err != nil {
		return fmt.Errorf("rebuild generation: %w", err)
	}

	switch result.Method {
	case "cached":
		out.Success(fmt.Sprintf("%s@%s already installed",
			result.Name, result.Version))
	case "source":
		out.Success(fmt.Sprintf(
			"Installed %s@%s (built from local source)",
			result.Name, result.Version))
	}

	return nil
}

// gitShortHash returns the short git commit hash for the
// given directory. Returns an error if the directory is not
// a git repository.
func gitShortHash(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--short", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse in %s: %w", dir, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// resolveRecipePath finds the recipe TOML file. If recipePath
// is provided, uses it directly. Otherwise, checks for a
// sibling gale-recipes directory next to sourceDir.
func resolveRecipePath(name, recipePath, sourceDir string) (string, error) {
	if recipePath != "" {
		return recipePath, nil
	}

	letter := string(name[0])
	sibling := filepath.Join(filepath.Dir(sourceDir), "gale-recipes")
	candidate := filepath.Join(sibling, "recipes", letter, name+".toml")
	if _, err := os.Stat(candidate); err == nil {
		return candidate, nil
	}

	return "", fmt.Errorf(
		"no recipe found for %q — use --recipe to specify a recipe file", name)
}

// findLocalRecipesDir finds a sibling gale-recipes directory
// relative to dir. Returns the path to the recipes/ subdirectory.
func findLocalRecipesDir(dir string) (string, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	recipesDir := filepath.Join(filepath.Dir(absDir), "gale-recipes", "recipes")
	if _, err := os.Stat(recipesDir); err != nil {
		return "", fmt.Errorf(
			"no sibling gale-recipes found next to %s", absDir)
	}
	return recipesDir, nil
}

// localRecipeResolver returns a RecipeResolver that reads
// recipes from a local recipes directory using letter-bucketed
// layout: <recipesDir>/<letter>/<name>.toml.
func localRecipeResolver(recipesDir string) installer.RecipeResolver {
	return func(name string) (*recipe.Recipe, error) {
		letter := string(name[0])
		path := filepath.Join(recipesDir, letter, name+".toml")
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf(
					"no local recipe for %q", name)
			}
			return nil, fmt.Errorf("read recipe %q: %w", name, err)
		}
		return recipe.Parse(string(data))
	}
}

func installFromRecipeFile(recipePath string, out *output.Output) error {
	data, err := os.ReadFile(recipePath)
	if err != nil {
		return fmt.Errorf("reading recipe: %w", err)
	}

	r, err := recipe.Parse(string(data))
	if err != nil {
		return fmt.Errorf("parsing recipe: %w", err)
	}

	galeDir, err := galeConfigDir()
	if err != nil {
		return err
	}

	storeRoot := defaultStoreRoot()

	inst := &installer.Installer{
		Store:    store.NewStore(storeRoot),
		Resolver: recipeFileResolver(recipePath),
	}

	out.Info(fmt.Sprintf("Installing %s@%s...",
		r.Package.Name, r.Package.Version))

	result, err := inst.Install(r)
	if err != nil {
		return fmt.Errorf("install failed: %w", err)
	}

	// Add to gale.toml and rebuild generation.
	configPath := filepath.Join(galeDir, "gale.toml")
	if err := config.AddPackage(configPath,
		r.Package.Name, r.Package.Version); err != nil {
		return fmt.Errorf("adding to config: %w", err)
	}
	if err := rebuildGeneration(galeDir, storeRoot,
		configPath); err != nil {
		return fmt.Errorf("rebuild generation: %w", err)
	}

	switch result.Method {
	case "cached":
		out.Success(fmt.Sprintf("%s@%s already installed",
			result.Name, result.Version))
	case "binary":
		out.Success(fmt.Sprintf("Installed %s@%s from binary",
			result.Name, result.Version))
	case "source":
		out.Success(fmt.Sprintf("Installed %s@%s (built from source)",
			result.Name, result.Version))
	}

	return nil
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

// recipeFileResolver returns a RecipeResolver that looks for
// recipes in the same repo as the given recipe file. Assumes
// letter-bucketed layout: recipes/<letter>/<name>.toml.
func recipeFileResolver(recipePath string) installer.RecipeResolver {
	absPath, err := filepath.Abs(recipePath)
	if err != nil {
		return nil
	}
	// recipePath is like .../recipes/j/jq.toml
	// We want the directory containing "recipes/".
	dir := filepath.Dir(filepath.Dir(filepath.Dir(absPath)))
	return localRecipeResolver(filepath.Join(dir, "recipes"))
}

// parsePackageArg splits "name@version" into name and version.
func parsePackageArg(arg string) (string, string) {
	if i := strings.LastIndex(arg, "@"); i > 0 {
		return arg[:i], arg[i+1:]
	}
	return arg, ""
}

// resolveConfigPath returns the gale.toml path to write to.
func resolveConfigPath(global bool) (string, error) {
	if global {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("finding home dir: %w", err)
		}
		return filepath.Join(home, ".gale", "gale.toml"), nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getting working dir: %w", err)
	}

	path, err := config.FindGaleConfig(cwd)
	if err == nil {
		return path, nil
	}

	return filepath.Join(cwd, "gale.toml"), nil
}
