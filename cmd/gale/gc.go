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

var gcCmd = &cobra.Command{
	Use:   "gc",
	Short: "Remove unused package versions from the store",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		out := output.New(os.Stderr, !cmd.Flags().Changed("no-color"))

		// Collect ALL referenced name@version pairs from
		// every config. A package may have different
		// versions in global vs project — keep both.
		referenced := map[string]bool{}

		// Global config.
		globalDir, err := galeConfigDir()
		if err == nil {
			mergeConfig(
				filepath.Join(globalDir, "gale.toml"),
				referenced)
		}

		// Project config.
		cwd, err := os.Getwd()
		var projPath string
		if err == nil {
			projPath, _ = config.FindGaleConfig(cwd)
			if projPath != "" {
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
			key := pkg.Name + "@" + pkg.Version
			if referenced[key] {
				continue
			}

			if dryRun {
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

		if dryRun {
			out.Info(fmt.Sprintf(
				"%d version(s) would be removed", removed))
			return nil
		}

		// Rebuild generation for the current scope.
		// If in a project, rebuild the project generation.
		// Always rebuild the global generation too.
		if projPath != "" {
			projRoot := filepath.Dir(projPath)
			projGaleDir := filepath.Join(projRoot, ".gale")
			if err := rebuildGeneration(projGaleDir,
				storeRoot, projPath); err != nil {
				return fmt.Errorf(
					"rebuild project generation: %w", err)
			}
		}
		if globalDir != "" {
			globalConfig := filepath.Join(
				globalDir, "gale.toml")
			if err := rebuildGeneration(globalDir,
				storeRoot, globalConfig); err != nil {
				return fmt.Errorf(
					"rebuild global generation: %w", err)
			}
		}

		out.Success(fmt.Sprintf(
			"Removed %d version(s)", removed))
		return nil
	},
}

// mergeConfig reads a gale.toml and adds its packages
// to the referenced set. Silently skips on errors.
func mergeConfig(path string, referenced map[string]bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	cfg, err := config.ParseGaleConfig(string(data))
	if err != nil {
		return
	}
	for name, version := range cfg.Packages {
		referenced[name+"@"+version] = true
	}
}

func init() {
	rootCmd.AddCommand(gcCmd)
}
