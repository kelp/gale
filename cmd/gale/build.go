package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kelp/gale/internal/build"
	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/store"
	"github.com/spf13/cobra"
)

var buildGit bool

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
			var resolver installer.RecipeResolver
			if recipesDir := detectRecipesRepo(recipePath); recipesDir != "" {
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
	buildCmd.Flags().BoolVar(&buildGit, "git", false,
		"Clone and build from git repository instead of tarball")
	rootCmd.AddCommand(buildCmd)
}

// detectRecipesRepo checks if the recipe file is inside a
// recipes repo (path contains /recipes/<letter>/<name>.toml).
// Returns the recipes root directory if detected, empty string
// otherwise.
func detectRecipesRepo(recipePath string) string {
	abs, err := filepath.Abs(recipePath)
	if err != nil {
		return ""
	}

	// Look for /recipes/<letter>/ in the path.
	normalized := filepath.ToSlash(abs)
	idx := strings.Index(normalized, "/recipes/")
	if idx < 0 {
		return ""
	}

	// Verify the structure: recipes/<single-char>/<name>.toml
	rest := normalized[idx+len("/recipes/"):]
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 2 {
		return ""
	}
	if len(parts[0]) != 1 {
		return ""
	}

	return filepath.FromSlash(normalized[:idx+len("/recipes")])
}
