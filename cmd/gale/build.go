package main

import (
	"fmt"
	"os"

	"github.com/kelp/gale/internal/build"
	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/store"
	"github.com/spf13/cobra"
)

var (
	buildLocal bool
	buildGit   bool
)

var buildCmd = &cobra.Command{
	Use:   "build <recipe.toml>",
	Short: "Build a package from source",
	Long:  "Build a package from a recipe file and produce a tar.zst archive.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		recipePath := args[0]
		out := output.New(os.Stderr, !cmd.Flags().Changed("no-color"))

		data, err := os.ReadFile(recipePath)
		if err != nil {
			return fmt.Errorf("reading recipe: %w", err)
		}

		r, err := recipe.Parse(string(data))
		if err != nil {
			return fmt.Errorf("parsing recipe: %w", err)
		}

		// Resolve and install build dependencies.
		var deps *build.BuildDeps
		if len(r.Dependencies.Build) > 0 {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("getting working dir: %w", err)
			}

			var resolver installer.RecipeResolver
			if buildLocal {
				recipesDir, err := findLocalRecipesDir(cwd)
				if err != nil {
					return err
				}
				resolver = localRecipeResolver(recipesDir)
			} else {
				reg := newRegistry()
				resolver = reg.FetchRecipe
			}

			inst := &installer.Installer{
				Store:    store.NewStore(defaultStoreRoot()),
				Resolver: resolver,
			}

			depPaths, err := inst.InstallBuildDeps(r)
			if err != nil {
				return fmt.Errorf("install build deps: %w", err)
			}
			deps = &build.BuildDeps{
				BinDirs:   depPaths.BinDirs,
				StoreDirs: depPaths.StoreDirs,
			}
		}

		outputDir, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working dir: %w", err)
		}

		if buildGit {
			out.Info(fmt.Sprintf("Building %s from git (%s)...",
				r.Package.Name, r.Source.Repo))
			result, hash, err := build.BuildGit(r, outputDir, deps)
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

		result, err := build.Build(r, outputDir, deps)
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
	buildCmd.Flags().BoolVar(&buildLocal, "local", false,
		"Resolve build dependencies from sibling gale-recipes directory")
	buildCmd.Flags().BoolVar(&buildGit, "git", false,
		"Clone and build from git repository instead of tarball")
	rootCmd.AddCommand(buildCmd)
}
