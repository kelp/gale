package main

import (
	"fmt"
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
	installRecipes string
	installRecipe  string
	installPath    string
	installGit     bool
	installBuild   bool
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

		// Resolve scope and paths via cmdContext.
		ctx, err := newCmdContext(installRecipes, installGlobal, installProject)
		if err != nil {
			return err
		}

		// If --path flag is provided, build from local source.
		if installPath != "" {
			if dryRun {
				out.Info(fmt.Sprintf(
					"install %s (from source)", name))
				return nil
			}
			return installFromLocalSource(ctx, name, installRecipe,
				installPath, out)
		}

		// If --git flag is provided, clone and build from git.
		if installGit {
			if dryRun {
				out.Info(fmt.Sprintf(
					"install %s (from git)", name))
				return nil
			}
			return installFromGit(ctx, name, installRecipe, out)
		}

		// If --recipe flag is provided, install from recipe file.
		if installRecipe != "" {
			if dryRun {
				out.Info(fmt.Sprintf(
					"install %s (from recipe)", name))
				return nil
			}
			return installFromRecipeFile(ctx, installRecipe, out)
		}

		out.Info(fmt.Sprintf("Fetching recipe for %s...", name))

		var r *recipe.Recipe
		if version != "" && version != "latest" && installRecipes == "" {
			// Specific version requested — fetch from
			// versioned registry index.
			reg := newRegistry()
			r, err = reg.FetchRecipeVersion(name, version)
			if err != nil {
				return fmt.Errorf("fetching %s@%s: %w",
					name, version, err)
			}
			// Use the registry for dep resolution too.
			ctx.Resolver = reg.FetchRecipe
			ctx.Installer.Resolver = reg.FetchRecipe
		} else {
			r, err = ctx.Resolver(name)
			if err != nil {
				return fmt.Errorf("fetching recipe: %w", err)
			}
		}

		if dryRun {
			out.Info(fmt.Sprintf("install %s@%s",
				r.Package.Name, r.Package.Version))
			return nil
		}

		if installBuild {
			ctx.Installer.SourceOnly = true
		}

		out.Info(fmt.Sprintf("Installing %s@%s...",
			r.Package.Name, r.Package.Version))

		result, err := ctx.Installer.Install(r)
		if err != nil {
			return fmt.Errorf("install failed: %w", err)
		}

		if err := ctx.FinalizeInstall(
			name, r.Package.Version, result.SHA256); err != nil {
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
	installCmd.Flags().StringVar(&installRecipes, "recipes", "",
		"Use local recipes directory (default: ../gale-recipes/)")
	installCmd.Flags().Lookup("recipes").NoOptDefVal = "auto"
	installCmd.Flags().StringVar(&installRecipe, "recipe", "",
		"Install from a recipe TOML file")
	installCmd.Flags().StringVar(&installPath, "path", "",
		"Build from a local source directory")
	installCmd.Flags().BoolVar(&installGit, "git", false,
		"Clone and build from git repository")
	installCmd.Flags().BoolVar(&installBuild, "build", false,
		"Build from source (skip prebuilt binary)")
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
// Returns true for global, false for project. When no
// flag is set, defaults to project if gale.toml exists
// in the directory tree, otherwise global.
func resolveScope(global, project bool, cwd string) bool {
	if global {
		return true
	}
	if project {
		return false
	}
	// Auto-detect: project config exists → project scope.
	_, err := config.FindGaleConfig(cwd)
	if err != nil {
		return true // no project config → global
	}
	return false // project config found → project scope
}

func installFromGit(ctx *cmdContext, name, recipePath string, out *output.Output) error {
	// When --recipe is provided, override resolver for
	// recipe lookup and dep resolution.
	resolver := ctx.Resolver
	if recipePath != "" {
		resolver = resolverForRecipe(recipePath)
	}

	// Resolve recipe.
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

	inst := &installer.Installer{
		Store:    store.NewStore(ctx.StoreRoot),
		Resolver: resolver,
		Verifier: attestation.NewVerifier(),
	}

	out.Info(fmt.Sprintf("Installing %s from git (%s)...",
		r.Package.Name, r.Source.Repo))

	result, err := inst.InstallGit(r)
	if err != nil {
		return fmt.Errorf("install failed: %w", err)
	}

	if err := ctx.FinalizeInstall(
		r.Package.Name, result.Version, result.SHA256); err != nil {
		return err
	}

	reportResult(out, result, "Installed", "built from git")

	return nil
}

func installFromLocalSource(ctx *cmdContext, name, recipePath, sourceDir string, out *output.Output) error {
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

	inst := newInstallerForRecipe(resolvedRecipe, ctx.StoreRoot)

	// Always rebuild local source — the source tree may have
	// changed without a version bump. Do not short-circuit
	// on IsInstalled for local builds.

	out.Info(fmt.Sprintf("Installing %s@%s from local source...",
		r.Package.Name, r.Package.Version))

	result, err := inst.InstallLocal(r, absSource)
	if err != nil {
		return fmt.Errorf("install failed: %w", err)
	}

	if err := ctx.FinalizeInstall(
		r.Package.Name, r.Package.Version, result.SHA256); err != nil {
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
//	"v0.2.0"                → "0.2.0"
//	"v0.2.0-7-g5395b8f"     → "0.2.0-dev.7+5395b8f"
//	"v1.0.0-rc1"            → "1.0.0-rc1"
//	"v1.0.0-rc1-3-gabcdef0" → "1.0.0-rc1-dev.3+abcdef0"
//	"5395b8f"               → "0.0.0-dev+5395b8f"
func formatDevVersion(describe string) string {
	// No tags: bare hash.
	if !strings.Contains(describe, ".") {
		return "0.0.0-dev+" + describe
	}

	// Strip leading "v".
	describe = strings.TrimPrefix(describe, "v")

	// When ahead of a tag, git describe appends -<N>-g<hex>.
	// Parse from the right to handle pre-release tags like
	// "1.0.0-rc1-3-gabcdef0".
	lastDash := strings.LastIndex(describe, "-")
	if lastDash < 0 {
		// Exactly on a tag: "0.2.0".
		return describe
	}

	suffix := describe[lastDash+1:]
	if !strings.HasPrefix(suffix, "g") {
		// No -g<hash> suffix — on a pre-release tag like
		// "1.0.0-rc1".
		return describe
	}

	// Find the commit count before the hash.
	rest := describe[:lastDash]
	countDash := strings.LastIndex(rest, "-")
	if countDash < 0 {
		// Malformed — treat as tag.
		return describe
	}

	tag := rest[:countDash]
	count := rest[countDash+1:]
	hash := strings.TrimPrefix(suffix, "g")
	return tag + "-dev." + count + "+" + hash
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

// resolverForRecipe returns a RecipeResolver for the given
// recipe file path. If the recipe is inside a letter-bucketed
// recipes repo, uses recipeFileResolver for local dep
// resolution. Otherwise falls back to the registry.
func resolverForRecipe(recipePath string) installer.RecipeResolver {
	if detectRecipesRepo(recipePath) != "" {
		return recipeFileResolver(recipePath)
	}
	return newRegistry().FetchRecipe
}

// newInstallerForRecipe constructs an Installer for
// installing from a recipe file or building from local
// source.
func newInstallerForRecipe(recipePath, storeRoot string) *installer.Installer {
	return &installer.Installer{
		Store:    store.NewStore(storeRoot),
		Resolver: resolverForRecipe(recipePath),
		Verifier: attestation.NewVerifier(),
	}
}

func installFromRecipeFile(ctx *cmdContext, recipePath string, out *output.Output) error {
	data, err := os.ReadFile(recipePath)
	if err != nil {
		return fmt.Errorf("reading recipe: %w", err)
	}

	r, err := recipe.Parse(string(data))
	if err != nil {
		return fmt.Errorf("parsing recipe: %w", err)
	}

	inst := newInstallerForRecipe(recipePath, ctx.StoreRoot)

	out.Info(fmt.Sprintf("Installing %s@%s...",
		r.Package.Name, r.Package.Version))

	result, err := inst.Install(r)
	if err != nil {
		return fmt.Errorf("install failed: %w", err)
	}

	if err := ctx.FinalizeInstall(
		r.Package.Name, r.Package.Version, result.SHA256); err != nil {
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
