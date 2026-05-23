package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var (
	whichGlobal  bool
	whichProject bool
)

var whichCmd = &cobra.Command{
	Use:   "which <binary>",
	Short: "Show which package provides a binary",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateScopeFlags(whichGlobal, whichProject); err != nil {
			return err
		}
		galeDir, _, err := resolveReadOnlyGaleDirForWhich(
			whichGlobal, whichProject)
		if err != nil {
			return err
		}

		storeRoot := defaultStoreRoot()

		name, version, resolved, err := resolveWhich(
			args[0], galeDir, storeRoot)
		if err != nil {
			return err
		}

		fmt.Printf("%s@%s\n", name, version)
		fmt.Println(resolved)
		return nil
	},
}

// resolveReadOnlyGaleDirForWhich returns the .gale dir used
// for binary lookup. Unlike resolveReadOnlyGaleDir, it does
// not require gale.toml to exist — `which` resolves against
// the generation symlinks, not the config.
func resolveReadOnlyGaleDirForWhich(global, project bool) (galeDir, configPath string, err error) {
	if global {
		galeDir, err = galeConfigDir()
		if err != nil {
			return "", "", err
		}
		configPath, err = globalConfigPath()
		return galeDir, configPath, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", "", fmt.Errorf("getting working dir: %w", err)
	}
	if project {
		projPath, err := projectConfigPath(cwd)
		if err != nil {
			return "", "", fmt.Errorf(
				"no project found — run 'gale init' first")
		}
		return filepath.Join(filepath.Dir(projPath), ".gale"), projPath, nil
	}
	// Auto.
	if projPath, err := projectConfigPath(cwd); err == nil {
		return filepath.Join(filepath.Dir(projPath), ".gale"), projPath, nil
	}
	galeDir, err = galeConfigDir()
	if err != nil {
		return "", "", err
	}
	configPath, err = globalConfigPath()
	return galeDir, configPath, err
}

// resolveWhich finds which package provides a binary by
// following symlinks from the current generation back to
// the store. Returns the package name, version, and
// resolved binary path.
func resolveWhich(binary, galeDir, storeRoot string) (string, string, string, error) {
	binPath := filepath.Join(
		galeDir, "current", "bin", binary)

	// Check the binary exists in the generation.
	if _, err := os.Lstat(binPath); err != nil {
		return "", "", "", fmt.Errorf(
			"%s not found in gale", binary)
	}

	// Resolve the full symlink chain to the store.
	resolved, err := filepath.EvalSymlinks(binPath)
	if err != nil {
		return "", "", "", fmt.Errorf(
			"resolving %s: %w", binary, err)
	}

	// Parse package name and version from the store path.
	// Store layout: <storeRoot>/<name>/<version>/bin/<binary>
	// EvalSymlinks on storeRoot too — on macOS /var is a
	// symlink to /private/var.
	cleanStore, err := filepath.EvalSymlinks(storeRoot)
	if err != nil {
		cleanStore = filepath.Clean(storeRoot)
	}
	cleanResolved := filepath.Clean(resolved)

	rel, err := filepath.Rel(cleanStore, cleanResolved)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", "", "", fmt.Errorf(
			"%s is not in the gale store", binary)
	}

	// rel is "<name>/<version>/bin/<binary>"
	sep := string(os.PathSeparator)
	parts := strings.SplitN(rel, sep, 4)
	if len(parts) < 4 || parts[2] != "bin" {
		return "", "", "", fmt.Errorf(
			"unexpected store path for %s", binary)
	}

	return parts[0], parts[1], resolved, nil
}

func init() {
	whichCmd.Flags().BoolVarP(&whichGlobal, "global", "g", false,
		"Look up the binary in the global generation")
	whichCmd.Flags().BoolVarP(&whichProject, "project", "p", false,
		"Look up the binary in the project generation")
	rootCmd.AddCommand(whichCmd)
}
