package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/store"
	"github.com/spf13/cobra"
)

var removeCmd = &cobra.Command{
	Use:   "remove <package>",
	Short: "Remove a package",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		out := output.New(os.Stderr, !cmd.Flags().Changed("no-color"))

		storeRoot := defaultStoreRoot()
		st := store.NewStore(storeRoot)

		// Find the config that declares this package.
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working dir: %w", err)
		}

		// Check project config first, then global.
		var configPath string
		var version string
		if projPath, err := config.FindGaleConfig(cwd); err == nil {
			data, _ := os.ReadFile(projPath)
			if cfg, err := config.ParseGaleConfig(
				string(data)); err == nil {
				if v, ok := cfg.Packages[name]; ok {
					configPath = projPath
					version = v
				}
			}
		}
		if configPath == "" {
			globalDir, err := galeConfigDir()
			if err != nil {
				return err
			}
			globalPath := filepath.Join(
				globalDir, "gale.toml")
			data, _ := os.ReadFile(globalPath)
			if cfg, err := config.ParseGaleConfig(
				string(data)); err == nil {
				if v, ok := cfg.Packages[name]; ok {
					configPath = globalPath
					version = v
				}
			}
		}

		if configPath == "" {
			return fmt.Errorf(
				"%s is not in any gale.toml", name)
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
	rootCmd.AddCommand(removeCmd)
}
