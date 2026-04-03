package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/generation"
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
		var removedPkgs int
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
			removedPkgs++
		}

		// Clean up old generations.
		var removedGens int
		if globalDir != "" {
			removedGens += cleanOldGenerations(
				globalDir, dryRun)
		}
		if projPath != "" {
			projGaleDir := filepath.Join(
				filepath.Dir(projPath), ".gale")
			removedGens += cleanOldGenerations(
				projGaleDir, dryRun)
		}

		if removedPkgs == 0 && removedGens == 0 {
			out.Success("Nothing to clean up.")
			return nil
		}

		if dryRun {
			out.Info(fmt.Sprintf(
				"%d version(s) and %d generation(s) "+
					"would be removed",
				removedPkgs, removedGens))
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
			"Removed %d version(s) and %d generation(s)",
			removedPkgs, removedGens))
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

// cleanOldGenerations removes all generation directories
// except the current one. Returns the count of generations
// removed (or flagged in dry-run mode).
func cleanOldGenerations(galeDir string, dry bool) int {
	out := output.New(os.Stderr, !noColor)
	genRoot := filepath.Join(galeDir, "gen")
	entries, err := os.ReadDir(genRoot)
	if err != nil {
		return 0
	}
	curGen, _ := generation.Current(galeDir)
	var removed int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		n, err := strconv.Atoi(e.Name())
		if err != nil || n == curGen {
			continue
		}
		genPath := filepath.Join(genRoot, e.Name())
		if dry {
			out.Info(fmt.Sprintf(
				"Would remove generation %d", n))
		} else {
			if err := os.RemoveAll(genPath); err != nil {
				out.Warn(fmt.Sprintf(
					"Failed to remove generation %d: %v",
					n, err))
				continue
			}
			out.Success(fmt.Sprintf(
				"Removed generation %d", n))
		}
		removed++
	}
	return removed
}

func init() {
	rootCmd.AddCommand(gcCmd)
}
