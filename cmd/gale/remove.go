package main

import (
	"fmt"
	"os"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/store"
	"github.com/spf13/cobra"
)

var (
	removeGlobal  bool
	removeProject bool
)

var removeCmd = &cobra.Command{
	Use:   "remove <package>",
	Short: "Remove a package",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if removeGlobal && removeProject {
			return fmt.Errorf(
				"cannot use both --global and --project")
		}

		name := args[0]

		out := output.New(os.Stderr, !cmd.Flags().Changed("no-color"))

		storeRoot := defaultStoreRoot()
		st := store.NewStore(storeRoot)

		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working dir: %w", err)
		}

		useGlobal := resolveScope(removeGlobal, removeProject, cwd)
		configPath, err := resolveConfigPath(useGlobal)
		if err != nil {
			return err
		}

		// Look up the package in the resolved config.
		var version string
		data, readErr := os.ReadFile(configPath)
		if readErr == nil {
			if cfg, parseErr := config.ParseGaleConfig(
				string(data)); parseErr == nil {
				if v, ok := cfg.Packages[name]; ok {
					version = v
				}
			}
		}

		if version == "" {
			return fmt.Errorf(
				"%s is not in %s", name, configPath)
		}

		if dryRun {
			out.Info(fmt.Sprintf(
				"remove %s@%s", name, version))
			return nil
		}

		// Remove only the declared version from the store.
		if st.IsInstalled(name, version) {
			if err := st.Remove(name, version); err != nil {
				return fmt.Errorf("removing from store: %w",
					err)
			}
			out.Info(fmt.Sprintf("Removed %s@%s from store",
				name, version))
		}

		// Remove from config.
		if err := config.RemovePackage(
			configPath, name); err != nil {
			return fmt.Errorf("removing from config: %w",
				err)
		}
		out.Info(fmt.Sprintf(
			"Removed %s from %s", name, configPath))

		// Rebuild the generation for this scope.
		galeDir, err := galeDirForConfig(configPath)
		if err != nil {
			return err
		}
		if err := rebuildGeneration(galeDir, storeRoot,
			configPath); err != nil {
			return fmt.Errorf("rebuild generation: %w", err)
		}

		out.Success(fmt.Sprintf("Removed %s", name))
		return nil
	},
}

func init() {
	removeCmd.Flags().BoolVarP(&removeGlobal, "global", "g",
		false, "Remove from global config")
	removeCmd.Flags().BoolVarP(&removeProject, "project", "p",
		false, "Remove from project config")
	rootCmd.AddCommand(removeCmd)
}
