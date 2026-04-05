package main

import (
	"fmt"
	"os"

	"github.com/kelp/gale/internal/attestation"
	"github.com/kelp/gale/internal/build"
	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/store"
	"github.com/spf13/cobra"
)

var (
	buildGit     bool
	buildDebug   bool
	buildRelease bool
	buildRecipes string
)

var buildCmd = &cobra.Command{
	Use:   "build <recipe.toml>",
	Short: "Build a package from source",
	Long:  "Build a package from a recipe file and produce a tar.zst archive.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		recipePath := args[0]
		out := output.New(os.Stderr, !cmd.Flags().Changed("no-color"))

		r, err := loadRecipeFile(recipePath, false)
		if err != nil {
			return err
		}

		// Resolve and install dependencies (build, runtime,
		// and implicit system deps). --recipes flag takes
		// precedence, then auto-detect from recipe path.
		cwd, cwdErr := os.Getwd()
		if cwdErr != nil {
			return fmt.Errorf("getting working dir: %w", cwdErr)
		}
		resolver, _, resolveErr := resolveRecipeResolver(buildRecipes, cwd)
		if resolveErr != nil {
			return resolveErr
		}
		// When no --recipes flag, check if the recipe file
		// itself is inside a recipes repo.
		if buildRecipes == "" {
			if recipesDir := detectRecipesRepo(recipePath); recipesDir != "" {
				resolver = localRecipeResolver(recipesDir)
			}
		}

		inst := &installer.Installer{
			Store:    store.NewStore(defaultStoreRoot()),
			Resolver: resolver,
			Verifier: attestation.NewVerifier(),
		}

		deps, err := inst.InstallBuildDeps(r)
		if err != nil {
			return fmt.Errorf("installing build deps: %w", err)
		}

		outputDir, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working dir: %w", err)
		}

		// Resolve debug mode: CLI > recipe > config > default.
		debug := resolveBuildDebug(r.Build.Debug,
			buildDebug, buildRelease)

		if buildGit {
			out.Info(fmt.Sprintf("Building %s from git (%s)...",
				r.Package.Name, r.Source.Repo))
			result, hash, err := build.BuildGit(r, outputDir, debug, deps)
			if err != nil {
				return fmt.Errorf("build failed: %w", err)
			}
			out.Success(fmt.Sprintf("Built %s@%s", r.Package.Name, hash))
			fmt.Printf("archive: %s\n", result.Archive)
			fmt.Printf("sha256:  %s\n", result.SHA256)
			return nil
		}

		out.Info(fmt.Sprintf("Building %s@%s from source...",
			r.Package.Name, r.Package.Version))

		result, err := build.Build(r, outputDir, debug, deps)
		if err != nil {
			return fmt.Errorf("build failed: %w", err)
		}

		out.Success(fmt.Sprintf("Built %s", result.Archive))
		fmt.Printf("archive: %s\n", result.Archive)
		fmt.Printf("sha256:  %s\n", result.SHA256)
		return nil
	},
}

func init() {
	buildCmd.Flags().BoolVar(&buildGit, "git", false,
		"Clone and build from git repository instead of tarball")
	buildCmd.Flags().BoolVar(&buildDebug, "debug", false,
		"Build with debug flags (-O0 -g)")
	buildCmd.Flags().BoolVar(&buildRelease, "release", false,
		"Build with release flags (overrides recipe debug)")
	buildCmd.Flags().StringVar(&buildRecipes, "recipes", "",
		"Use local recipes directory (default: ../gale-recipes/)")
	buildCmd.Flags().Lookup("recipes").NoOptDefVal = "auto"
	rootCmd.AddCommand(buildCmd)
}
