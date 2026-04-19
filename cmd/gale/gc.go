package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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
		out := newCmdOutput(cmd)

		// Resolve config paths.
		globalDir, _ := galeConfigDir()
		var projPath string
		if cwd, err := os.Getwd(); err == nil {
			projPath, _ = config.FindGaleConfig(cwd)
		}

		// Collect all referenced name@version pairs.
		referenced := collectReferencedPackages(
			globalDir, projPath, out)

		// Remove unreferenced package versions.
		storeRoot := defaultStoreRoot()
		s := store.NewStore(storeRoot)
		removedPkgs := removeUnreferencedVersions(
			s, referenced, dryRun, out)

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

// isReferenced reports whether a store entry is kept by
// the config-derived reference set. Matches both exact
// name@version (user pinned a revision: jq = "1.8.1-2")
// and name@base-version (user wrote a bare version:
// jq = "1.8.1" covers jq@1.8.1-2 in the store). The bare
// form mirrors the resolver's back-compat lookup — anything
// the installer treats as a match for the user's pin must
// also be treated as referenced by gc, or gc will delete
// live store entries out from under the active generation.
func isReferenced(name, version string, referenced map[string]bool) bool {
	if referenced[name+"@"+version] {
		return true
	}
	if base, _, ok := strings.Cut(version, "-"); ok {
		return referenced[name+"@"+base]
	}
	return false
}

// removeUnreferencedVersions iterates the store and
// removes any version not in the referenced set.
// Returns the number of versions removed (or flagged
// in dry-run mode).
func removeUnreferencedVersions(
	s *store.Store,
	referenced map[string]bool,
	dry bool,
	out *output.Output,
) int {
	installed, err := s.List()
	if err != nil {
		out.Warn(fmt.Sprintf("listing store: %v", err))
		return 0
	}
	var removed int
	for _, pkg := range installed {
		if isReferenced(pkg.Name, pkg.Version, referenced) {
			continue
		}
		if dry {
			out.Info(fmt.Sprintf(
				"Would remove %s@%s",
				pkg.Name, pkg.Version))
		} else {
			if err := s.Remove(
				pkg.Name, pkg.Version); err != nil {
				out.Warn(fmt.Sprintf(
					"Failed to remove %s@%s: %v",
					pkg.Name, pkg.Version, err))
				continue
			}
			out.Success(fmt.Sprintf(
				"Removed %s@%s",
				pkg.Name, pkg.Version))
		}
		removed++
	}
	return removed
}

// collectReferencedPackages merges all name@version
// pairs from global and project configs into a set.
// Silently skips missing configs but warns on parse errors.
func collectReferencedPackages(
	globalDir, projPath string, out *output.Output,
) map[string]bool {
	referenced := map[string]bool{}
	if globalDir != "" {
		mergeConfig(
			filepath.Join(globalDir, "gale.toml"),
			referenced, out)
	}
	if projPath != "" {
		mergeConfig(projPath, referenced, out)
	}
	return referenced
}

// mergeConfig reads a gale.toml and adds its packages
// to the referenced set. Silently skips missing files
// but warns on parse errors.
func mergeConfig(path string, referenced map[string]bool, out *output.Output) {
	data, err := os.ReadFile(path)
	if err != nil {
		return // missing config is fine
	}
	cfg, err := config.ParseGaleConfig(string(data))
	if err != nil {
		out.Warn(fmt.Sprintf("parsing %s: %v", path, err))
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
	out := newOutput()
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
