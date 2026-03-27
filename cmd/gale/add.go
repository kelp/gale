package main

import (
	"fmt"
	"os"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/output"
	"github.com/spf13/cobra"
)

var (
	addGlobal  bool
	addProject bool
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
		reg := newRegistry()

		// Default to project scope for add.
		configPath, err := resolveConfigPath(addGlobal)
		if err != nil {
			return err
		}

		for _, arg := range args {
			name, version := parsePackageArg(arg)

			// Validate recipe exists.
			r, err := reg.FetchRecipe(name)
			if err != nil {
				return fmt.Errorf("validating %s: %w", name, err)
			}

			if err := checkVersionMatch(version, r.Package.Version); err != nil {
				return err
			}

			actualVersion := r.Package.Version
			if err := config.AddPackage(configPath, name,
				actualVersion); err != nil {
				return fmt.Errorf("adding %s: %w", name, err)
			}

			out.Success(fmt.Sprintf("Added %s@%s to %s",
				name, actualVersion, configPath))
		}

		return nil
	},
}

func init() {
	addCmd.Flags().BoolVarP(&addGlobal, "global", "g",
		false, "Add to global config")
	addCmd.Flags().BoolVarP(&addProject, "project", "p",
		false, "Add to project config")
	rootCmd.AddCommand(addCmd)
}
