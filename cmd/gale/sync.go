package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/store"
	"github.com/spf13/cobra"
)

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Install all packages in gale.toml",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		out := output.New(os.Stderr, !cmd.Flags().Changed("no-color"))

		// Find config: project gale.toml first, fall back
		// to global ~/.gale/gale.toml.
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working dir: %w", err)
		}

		galePath, err := config.FindGaleConfig(cwd)
		if err != nil {
			// No project config — use global.
			globalDir, dirErr := galeConfigDir()
			if dirErr != nil {
				return dirErr
			}
			galePath = filepath.Join(globalDir, "gale.toml")
		}

		data, err := os.ReadFile(galePath)
		if err != nil {
			return fmt.Errorf("reading %s: %w", galePath, err)
		}

		cfg, err := config.ParseGaleConfig(string(data))
		if err != nil {
			return fmt.Errorf("parsing config: %w", err)
		}

		if len(cfg.Packages) == 0 {
			out.Info("No packages to sync.")
			return nil
		}

		// Determine gale dir: if config is in a project,
		// use project's .gale/. Otherwise use ~/.gale/.
		galeDir, err := galeConfigDir()
		if err != nil {
			return err
		}
		configDir := filepath.Dir(galePath)
		globalDir, _ := galeConfigDir()
		if configDir != globalDir {
			// Project config — use .gale/ next to it.
			galeDir = filepath.Join(configDir, ".gale")
		}

		// Set up registry and installer.
		reg := newRegistry()
		storeRoot := defaultStoreRoot()

		inst := &installer.Installer{
			Store:    store.NewStore(storeRoot),
			Resolver: reg.FetchRecipe,
		}

		var installed int
		for name := range cfg.Packages {
			r, err := reg.FetchRecipe(name)
			if err != nil {
				out.Warn(fmt.Sprintf(
					"Skipping %s: %v", name, err))
				continue
			}

			out.Info(fmt.Sprintf("Installing %s@%s...",
				r.Package.Name, r.Package.Version))

			result, err := inst.Install(r)
			if err != nil {
				out.Warn(fmt.Sprintf(
					"Failed to install %s: %v", name, err))
				continue
			}

			switch result.Method {
			case "cached":
				out.Info(fmt.Sprintf(
					"%s@%s already installed",
					result.Name, result.Version))
			case "binary":
				out.Success(fmt.Sprintf(
					"Installed %s@%s from binary",
					result.Name, result.Version))
				installed++
			case "source":
				out.Success(fmt.Sprintf(
					"Installed %s@%s (built from source)",
					result.Name, result.Version))
				installed++
			}
		}

		// Rebuild generation from gale.toml.
		if err := rebuildGeneration(galeDir, storeRoot,
			galePath); err != nil {
			return fmt.Errorf("rebuild generation: %w", err)
		}

		out.Success(fmt.Sprintf(
			"Sync complete: %d packages installed", installed))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(syncCmd)
}
