package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/store"
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
		galeDir, err := resolveReadOnlyGaleDirForWhich(
			whichGlobal, whichProject,
		)
		if err != nil {
			return err
		}

		storeRoot := defaultStoreRoot()

		got, err := resolveWhich(
			args[0], galeDir, storeRoot,
		)
		if err != nil {
			return err
		}

		fmt.Printf("%s@%s\n", got.name, got.version)
		fmt.Println(got.path)
		if others := otherProviders(
			args[0], got.name, galeDir, storeRoot,
		); len(others) > 0 {
			fmt.Printf("also provided by: %s\n", strings.Join(others, ", "))
		}
		return nil
	},
}

// otherProviders returns the packages in the active generation,
// besides winner, that also ship bin/<binary>.
//
// The answer comes from the store, not the generation: a shadowed
// provider's entry was never linked, so the generation is exactly
// where it cannot be seen. A collision now refuses the rebuild
// (gh#190). The remaining way to reach this state is a pre-upgrade
// generation that already linked one copy while another declared
// package still ships the same basename.
//
// Best effort: an unreadable generation costs the extra line, never
// the answer `which` was asked for.
func otherProviders(binary, winner, galeDir, storeRoot string) []string {
	active, err := generation.CurrentVersions(galeDir, storeRoot)
	if err != nil {
		return nil
	}
	s := store.NewStore(storeRoot)
	var out []string
	for name, version := range active {
		if name == winner {
			continue
		}
		if _, err := os.Stat(filepath.Join(
			s.ResolveDir(name, version), "bin", binary,
		)); err == nil {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// resolveReadOnlyGaleDirForWhich returns the .gale dir used
// for binary lookup. Does not require gale.toml to exist —
// `which` resolves against the generation symlinks, not the
// config. Uses galeDirForConfig so the gh#96 guard applies.
func resolveReadOnlyGaleDirForWhich(global, project bool) (string, error) {
	galeDir, _, err := resolveScopedPaths(global, project)
	return galeDir, err
}

// whichResult is the package that provides a binary.
type whichResult struct {
	name, version, path string
}

// resolveWhich finds which package provides a binary by
// following symlinks from the current generation back to
// the store. Returns the package name, version, and
// resolved binary path.
func resolveWhich(binary, galeDir, storeRoot string) (whichResult, error) {
	binPath := filepath.Join(
		galeDir, "current", "bin", binary,
	)

	// Check the binary exists in the generation.
	if _, err := os.Lstat(binPath); err != nil {
		return whichResult{}, fmt.Errorf(
			"%s not found in gale", binary,
		)
	}

	// Resolve the full symlink chain to the store.
	resolved, err := filepath.EvalSymlinks(binPath)
	if err != nil {
		return whichResult{}, fmt.Errorf(
			"resolving %s: %w", binary, err,
		)
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
		return whichResult{}, fmt.Errorf(
			"%s is not in the gale store", binary,
		)
	}

	// rel is "<name>/<version>/bin/<binary>"
	sep := string(os.PathSeparator)
	parts := strings.SplitN(rel, sep, 4)
	if len(parts) < 4 || parts[2] != "bin" {
		return whichResult{}, fmt.Errorf(
			"unexpected store path for %s", binary,
		)
	}

	return whichResult{parts[0], parts[1], resolved}, nil
}

func init() {
	whichCmd.Flags().BoolVarP(&whichGlobal, "global", "g", false,
		"Look up the binary in the global generation")
	whichCmd.Flags().BoolVarP(&whichProject, "project", "p", false,
		"Look up the binary in the project generation")
	rootCmd.AddCommand(whichCmd)
}
