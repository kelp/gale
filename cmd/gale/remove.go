package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/profile"
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

		galeDir, err := galeConfigDir()
		if err != nil {
			return err
		}

		storeRoot := defaultStoreRoot()
		binDir := filepath.Join(galeDir, "bin")
		st := store.NewStore(storeRoot)
		prof := profile.NewProfile(binDir)

		// Find installed versions of this package.
		pkgs, err := st.List()
		if err != nil {
			return fmt.Errorf("listing packages: %w", err)
		}

		var removed bool
		for _, pkg := range pkgs {
			if pkg.Name != name {
				continue
			}

			storeDir := filepath.Join(storeRoot,
				pkg.Name, pkg.Version)

			// Remove symlinks pointing into this store dir.
			if err := prof.UnlinkPackageBinaries(storeDir); err != nil {
				out.Warn(fmt.Sprintf(
					"failed to unlink binaries: %v", err))
			}

			// Remove from store.
			if err := st.Remove(pkg.Name, pkg.Version); err != nil {
				return fmt.Errorf("removing from store: %w", err)
			}

			out.Info(fmt.Sprintf("Removed %s@%s from store",
				pkg.Name, pkg.Version))
			removed = true
		}

		// Remove from gale.toml (best effort — may not be
		// in config if installed via --recipe).
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working dir: %w", err)
		}

		configPath, err := config.FindGaleConfig(cwd)
		if err == nil {
			if err := config.RemovePackage(
				configPath, name); err == nil {
				out.Info(fmt.Sprintf(
					"Removed %s from %s", name, configPath))
			}
		}

		// Also try global config.
		globalConfig := filepath.Join(galeDir, "gale.toml")
		if globalConfig != configPath {
			if err := config.RemovePackage(
				globalConfig, name); err == nil {
				out.Info(fmt.Sprintf(
					"Removed %s from %s", name, globalConfig))
			}
		}

		if removed {
			out.Success(fmt.Sprintf("Removed %s", name))
		} else {
			return fmt.Errorf("%s is not installed", name)
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(removeCmd)
}
