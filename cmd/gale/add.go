package main

import (
	"fmt"
	"os"

	"github.com/kelp/gale/internal/output"
	"github.com/spf13/cobra"
)

var (
	addGlobal  bool
	addProject bool
	addRecipes string
)

var addCmd = &cobra.Command{
	Use:   "add <package>[@version] [package...]",
	Short: "Add packages to gale.toml without installing",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateInstallFlags(addGlobal, addProject); err != nil {
			return err
		}

		out := output.New(os.Stderr, !cmd.Flags().Changed("no-color"))

		// Set up resolver for version lookup.
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working dir: %w", err)
		}

		var resolver func(string) (string, error)
		if addRecipes != "" {
			override := ""
			if addRecipes != "auto" {
				override = addRecipes
			}
			recipesDir, dirErr := findLocalRecipesDir(cwd, override)
			if dirErr != nil {
				return dirErr
			}
			localRes := localRecipeResolver(recipesDir)
			resolver = func(name string) (string, error) {
				r, err := localRes(name)
				if err != nil {
					return "", err
				}
				return r.Package.Version, nil
			}
		} else {
			reg := newRegistry()
			resolver = func(name string) (string, error) {
				r, err := reg.FetchRecipe(name)
				if err != nil {
					return "", err
				}
				return r.Package.Version, nil
			}
		}

		for _, arg := range args {
			name, version := parsePackageArg(arg)

			// If @version specified, trust the user.
			if version == "" {
				resolved, err := resolver(name)
				if err != nil {
					return fmt.Errorf("resolving %s: %w",
						name, err)
				}
				version = resolved
			}

			configPath, err := addToConfig(
				name, version, addGlobal, addProject)
			if err != nil {
				return err
			}

			out.Success(fmt.Sprintf("Added %s@%s to %s",
				name, version, configPath))
		}

		return nil
	},
}

func init() {
	addCmd.Flags().BoolVarP(&addGlobal, "global", "g",
		false, "Add to global config")
	addCmd.Flags().BoolVarP(&addProject, "project", "p",
		false, "Add to project config")
	addCmd.Flags().StringVar(&addRecipes, "recipes", "",
		"Use local recipes directory (default: ../gale-recipes/)")
	addCmd.Flags().Lookup("recipes").NoOptDefVal = "auto"
	rootCmd.AddCommand(addCmd)
}
