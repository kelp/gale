package main

import (
	"fmt"
	"os"

	"github.com/kelp/gale/internal/config"
	"github.com/spf13/cobra"
)

var (
	addGlobal  bool
	addProject bool
	addRecipes string
	addHost    string
)

var addCmd = &cobra.Command{
	Use:   "add <package>[@version] [package...]",
	Short: "Add packages to gale.toml without installing",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateInstallFlags(addGlobal, addProject); err != nil {
			return err
		}

		out := newCmdOutput(cmd)

		// Set up resolver for version lookup.
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working dir: %w", err)
		}

		if addProject {
			if _, err := projectConfigPath(cwd); err != nil {
				return fmt.Errorf("no project found — run 'gale init' first")
			}
		}

		recipeRes, _, resolveErr := resolveRecipeResolver(addRecipes, cwd)
		if resolveErr != nil {
			return resolveErr
		}
		resolver := func(name string) (string, error) {
			r, err := recipeRes(name)
			if err != nil {
				return "", err
			}
			return r.Package.Version, nil
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

			host := resolveHostFlag(addHost)

			if dryRun {
				useGlobal := resolveScope(addGlobal, addProject, cwd)
				configPath, err := resolveConfigPath(useGlobal)
				if err != nil {
					return err
				}
				location := configPath
				if host != "" {
					location = fmt.Sprintf("%s [hosts.%s]",
						configPath, host)
				}
				out.Info(fmt.Sprintf("add %s@%s to %s",
					name, version, location))
				continue
			}

			configPath, err := addToConfig(
				name, version, host, addGlobal, addProject)
			if err != nil {
				return err
			}

			location := configPath
			if host != "" {
				location = fmt.Sprintf("%s [hosts.%s]",
					configPath, host)
			}
			out.Success(fmt.Sprintf("Added %s@%s to %s",
				name, version, location))
		}

		return nil
	},
}

// resolveHostFlag turns a --host CLI value into a host name.
// Empty string means shared [packages]. "current" expands to
// the local hostname.
func resolveHostFlag(v string) string {
	if v == "current" {
		return config.CurrentHost()
	}
	return v
}

func init() {
	addCmd.Flags().BoolVarP(&addGlobal, "global", "g",
		false, "Add to global config")
	addCmd.Flags().BoolVarP(&addProject, "project", "p",
		false, "Add to project config")
	addCmd.Flags().StringVar(&addRecipes, "recipes", "",
		"Use local recipes directory (default: ../gale-recipes/)")
	addCmd.Flags().Lookup("recipes").NoOptDefVal = "auto"
	addCmd.Flags().StringVar(&addHost, "host", "",
		"Write under [hosts.<host>.packages] "+
			"(use 'current' for this machine)")
	rootCmd.AddCommand(addCmd)
}
