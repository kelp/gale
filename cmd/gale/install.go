package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kelp/gale/internal/attestation"
	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/store"
	"github.com/spf13/cobra"
)

var (
	installGlobal  bool
	installProject bool
	installLocal   bool
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

		// Resolve scope and paths up front — all branches
		// use the same config path.
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
		galeDir, err := galeConfigDir()
		if err != nil {
			return err
		}
		storeRoot := defaultStoreRoot()

		// If --source flag is provided, build from local source.
		if installSource != "" {
			return installFromLocalSource(name, installRecipe,
				installSource, configPath, galeDir, storeRoot, out)
		}

		// If --git flag is provided, clone and build from git.
		if installGit {
			return installFromGit(name, installRecipe,
				configPath, galeDir, storeRoot, installLocal, out)
		}

		// If --recipe flag is provided, install from recipe file.
		if installRecipe != "" {
			return installFromRecipeFile(installRecipe,
				configPath, galeDir, storeRoot, out)
		}

		// Fetch recipe from registry or local recipes.
		var resolver installer.RecipeResolver
		if installLocal {
			recipesDir, dirErr := findLocalRecipesDir(cwd)
			if dirErr != nil {
				return dirErr
			}
			resolver = localRecipeResolver(recipesDir)
		} else {
			resolver = newRegistry().FetchRecipe
		}

		out.Info(fmt.Sprintf("Fetching recipe for %s...", name))

		var r *recipe.Recipe
		if version != "" && version != "latest" && !installLocal {
			// Specific version requested — fetch from
			// versioned registry index.
			reg := newRegistry()
			r, err = reg.FetchRecipeVersion(name, version)
			if err != nil {
				return fmt.Errorf("fetching %s@%s: %w",
					name, version, err)
			}
			// Use the registry for dep resolution too.
			resolver = reg.FetchRecipe
		} else {
			r, err = resolver(name)
			if err != nil {
				return fmt.Errorf("fetching recipe: %w", err)
			}
		}

		inst := &installer.Installer{
			Store:    store.NewStore(storeRoot),
			Resolver: resolver,
			Verifier: attestation.NewVerifier(),
		}

		out.Info(fmt.Sprintf("Installing %s@%s...",
			r.Package.Name, r.Package.Version))

		result, err := inst.Install(r)
		if err != nil {
			return fmt.Errorf("install failed: %w", err)
		}

		if err := finalizeInstall(galeDir, storeRoot,
			configPath, name, r.Package.Version, result.SHA256); err != nil {
			return err
		}

		reportResult(out, result, "Installed", "built from source")

		return nil
	},
}

func init() {
	installCmd.Flags().BoolVarP(&installGlobal, "global", "g",
		false, "Install to global config")
	installCmd.Flags().BoolVarP(&installProject, "project", "p",
		false, "Install to project config")
	installCmd.Flags().BoolVar(&installLocal, "local", false,
		"Resolve recipes from sibling gale-recipes directory")
	installCmd.Flags().StringVar(&installRecipe, "recipe", "",
		"Install from a recipe TOML file")
	installCmd.Flags().StringVar(&installSource, "source", "",
		"Build from a local source directory")
	installCmd.Flags().BoolVar(&installGit, "git", false,
		"Clone and build from git repository")
	rootCmd.AddCommand(installCmd)
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

func installFromGit(name, recipePath, configPath, galeDir, storeRoot string, local bool, out *output.Output) error {
	// Resolve recipe from registry, local, or --recipe flag.
	var r *recipe.Recipe
	if recipePath != "" {
		data, err := os.ReadFile(recipePath)
		if err != nil {
			return fmt.Errorf("reading recipe: %w", err)
		}
		parsed, err := recipe.ParseLocal(string(data))
		if err != nil {
			return fmt.Errorf("parsing recipe: %w", err)
		}
		r = parsed
	} else {
		var resolver installer.RecipeResolver
		if local {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("getting working dir: %w", err)
			}
			recipesDir, err := findLocalRecipesDir(cwd)
			if err != nil {
				return err
			}
			resolver = localRecipeResolver(recipesDir)
		} else {
			resolver = newRegistry().FetchRecipe
		}
		fetched, err := resolver(name)
		if err != nil {
			return fmt.Errorf("fetching recipe: %w", err)
		}
		r = fetched
	}

	if r.Source.Repo == "" {
		return fmt.Errorf(
			"recipe for %s has no source.repo — cannot build from git", name)
	}

	// Use the same resolver for build dep resolution.
	var depResolver installer.RecipeResolver
	if local {
		cwd, _ := os.Getwd()
		recipesDir, _ := findLocalRecipesDir(cwd)
		depResolver = localRecipeResolver(recipesDir)
	} else {
		depResolver = newRegistry().FetchRecipe
	}

	inst := &installer.Installer{
		Store:    store.NewStore(storeRoot),
		Resolver: depResolver,
		Verifier: attestation.NewVerifier(),
	}

	out.Info(fmt.Sprintf("Installing %s from git (%s)...",
		r.Package.Name, r.Source.Repo))

	result, err := inst.InstallGit(r)
	if err != nil {
		return fmt.Errorf("install failed: %w", err)
	}

	if err := finalizeInstall(galeDir, storeRoot,
		configPath, r.Package.Name, result.Version,
		result.SHA256); err != nil {
		return err
	}

	reportResult(out, result, "Installed", "built from git")

	return nil
}

func installFromLocalSource(name, recipePath, sourceDir, configPath, galeDir, storeRoot string, out *output.Output) error {
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

	// Override version with semver dev version from git.
	version, err := gitDevVersion(absSource)
	if err != nil {
		return fmt.Errorf("detecting version: %w", err)
	}
	r.Package.Version = version

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

	if err := finalizeInstall(galeDir, storeRoot,
		configPath, r.Package.Name, r.Package.Version,
		result.SHA256); err != nil {
		return err
	}

	reportResult(out, result, "Installed", "built from local source")

	return nil
}

// gitDevVersion returns a semver-compliant version string
// for the given git directory. Uses git describe to find the
// nearest tag and formats as:
//   - "0.2.0" when exactly on tag v0.2.0
//   - "0.2.0-dev.7+5395b8f" when 7 commits ahead
//   - "0.0.0-dev+5395b8f" when no tags exist
func gitDevVersion(dir string) (string, error) {
	cmd := exec.Command("git", "describe",
		"--tags", "--always")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git describe in %s: %w", dir, err)
	}
	return formatDevVersion(strings.TrimSpace(string(out))), nil
}

// formatDevVersion converts git describe output to semver.
//
//	"v0.2.0"            → "0.2.0"
//	"v0.2.0-7-g5395b8f" → "0.2.0-dev.7+5395b8f"
//	"5395b8f"           → "0.0.0-dev+5395b8f"
func formatDevVersion(describe string) string {
	// No tags: bare hash.
	if !strings.Contains(describe, ".") {
		return "0.0.0-dev+" + describe
	}

	// Strip leading "v".
	describe = strings.TrimPrefix(describe, "v")

	// Exactly on a tag: "0.2.0".
	parts := strings.SplitN(describe, "-", 3)
	if len(parts) == 1 {
		return describe
	}

	// Ahead of tag: "0.2.0-7-g5395b8f".
	// parts[0]="0.2.0", parts[1]="7", parts[2]="g5395b8f"
	hash := strings.TrimPrefix(parts[2], "g")
	return parts[0] + "-dev." + parts[1] + "+" + hash
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

func installFromRecipeFile(recipePath, configPath, galeDir, storeRoot string, out *output.Output) error {
	data, err := os.ReadFile(recipePath)
	if err != nil {
		return fmt.Errorf("reading recipe: %w", err)
	}

	r, err := recipe.Parse(string(data))
	if err != nil {
		return fmt.Errorf("parsing recipe: %w", err)
	}

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

	if err := finalizeInstall(galeDir, storeRoot,
		configPath, r.Package.Name, r.Package.Version,
		result.SHA256); err != nil {
		return err
	}

	reportResult(out, result, "Installed", "built from source")

	return nil
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
