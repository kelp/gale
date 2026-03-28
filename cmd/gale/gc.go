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

var gcDryRun bool

var gcCmd = &cobra.Command{
	Use:   "gc",
	Short: "Remove unused package versions from the store",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		out := output.New(os.Stderr, !cmd.Flags().Changed("no-color"))

		// Collect referenced versions from all configs.
		referenced := map[string]string{}

		// Global config.
		globalDir, err := galeConfigDir()
		if err == nil {
			mergeConfig(
				filepath.Join(globalDir, "gale.toml"),
				referenced)
		}

		// Project config.
		cwd, err := os.Getwd()
		if err == nil {
			if projPath, err := config.FindGaleConfig(cwd); err == nil {
				mergeConfig(projPath, referenced)
			}
		}

		// List all installed versions.
		storeRoot := defaultStoreRoot()
		s := store.NewStore(storeRoot)
		installed, err := s.List()
		if err != nil {
			return fmt.Errorf("listing store: %w", err)
		}

		// Find unreferenced versions.
		var removed int
		for _, pkg := range installed {
			if referenced[pkg.Name] == pkg.Version {
				continue
			}

			if gcDryRun {
				out.Info(fmt.Sprintf(
					"Would remove %s@%s", pkg.Name, pkg.Version))
			} else {
				if err := s.Remove(pkg.Name, pkg.Version); err != nil {
					out.Warn(fmt.Sprintf(
						"Failed to remove %s@%s: %v",
						pkg.Name, pkg.Version, err))
					continue
				}
				out.Success(fmt.Sprintf(
					"Removed %s@%s", pkg.Name, pkg.Version))
			}
			removed++
		}

		if removed == 0 {
			out.Success("Nothing to clean up.")
			return nil
		}

		if gcDryRun {
			out.Info(fmt.Sprintf(
				"%d version(s) would be removed", removed))
			return nil
		}

		// Rebuild generation after cleanup.
		galeDir, err := galeConfigDir()
		if err != nil {
			return err
		}
		configPath := filepath.Join(galeDir, "gale.toml")
		if err := rebuildGeneration(galeDir, storeRoot,
			configPath); err != nil {
			return fmt.Errorf("rebuild generation: %w", err)
		}

		out.Success(fmt.Sprintf(
			"Removed %d version(s)", removed))
		return nil
	},
}

// mergeConfig reads a gale.toml and merges its packages
// into the referenced map. Silently skips on errors.
func mergeConfig(path string, referenced map[string]string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	cfg, err := config.ParseGaleConfig(string(data))
	if err != nil {
		return
	}
	for name, version := range cfg.Packages {
		referenced[name] = version
	}
}

func init() {
	gcCmd.Flags().BoolVar(&gcDryRun, "dry-run", false,
		"Show what would be removed without removing")
	rootCmd.AddCommand(gcCmd)
}
