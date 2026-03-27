package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/profile"
	"github.com/kelp/gale/internal/store"
	"github.com/spf13/cobra"
)

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Install all packages in gale.toml",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		out := output.New(os.Stderr, !cmd.Flags().Changed("no-color"))

		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working dir: %w", err)
		}

		galePath, err := config.FindGaleConfig(cwd)
		if err != nil {
			return fmt.Errorf("no gale.toml found: %w", err)
		}

		data, err := os.ReadFile(galePath)
		if err != nil {
			return fmt.Errorf("reading config: %w", err)
		}

		cfg, err := config.ParseGaleConfig(string(data))
		if err != nil {
			return fmt.Errorf("parsing config: %w", err)
		}

		if len(cfg.Packages) == 0 {
			out.Info("No packages to sync.")
			return nil
		}

		// Set up registry and installer.
		reg := newRegistry()

		galeDir, err := galeConfigDir()
		if err != nil {
			return err
		}

		storeRoot := defaultStoreRoot()
		binDir := filepath.Join(galeDir, "bin")

		inst := &installer.Installer{
			Store:    store.NewStore(storeRoot),
			Profile:  profile.NewProfile(binDir),
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

		out.Success(fmt.Sprintf(
			"Sync complete: %d packages installed", installed))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(syncCmd)
}
