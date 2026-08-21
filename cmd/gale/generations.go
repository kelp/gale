package main

import (
	"fmt"
	"strconv"

	"github.com/kelp/gale/internal/generation"
	"github.com/spf13/cobra"
)

var (
	generationsGlobal  bool
	generationsProject bool
)

var generationsCmd = &cobra.Command{
	Use:   "generations",
	Short: "List generations or roll back one step",
	// ExactArgs(0): bare `gale generations` lists; rollback
	// handles its own arg shape. Falling through to here
	// with an unrecognised positional (e.g. `gale generations
	// nosuchcmd`) must reject it cleanly rather than echo
	// cobra's stock "unknown command" line.
	Args: cobra.ExactArgs(0),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateScopeFlags(generationsGlobal, generationsProject); err != nil {
			return err
		}
		galeDir, err := resolveGenerationsGaleDir(
			generationsGlobal, generationsProject,
		)
		if err != nil {
			return err
		}

		storeRoot := defaultStoreRoot()
		gens, err := generation.List(galeDir, storeRoot)
		if err != nil {
			return fmt.Errorf("listing generations: %w", err)
		}

		if len(gens) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(),
				"No generations found.")
			return nil
		}

		cur := currentGenNumber(gens)
		for _, g := range gens {
			fmt.Fprintf(cmd.OutOrStdout(),
				"%s %-3d %d packages\n",
				genMarker(g, cur), g.Number, len(g.Packages))
		}

		return nil
	},
}

var genRollbackCmd = &cobra.Command{
	Use:   "rollback [N]",
	Short: "Switch to a previous generation",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateScopeFlags(generationsGlobal, generationsProject); err != nil {
			return err
		}
		galeDir, err := resolveGenerationsGaleDir(
			generationsGlobal, generationsProject,
		)
		if err != nil {
			return err
		}

		out := newCmdOutput(cmd)

		cur, err := generation.Current(galeDir)
		if err != nil {
			return fmt.Errorf("reading current: %w", err)
		}
		if cur == 0 {
			return fmt.Errorf("no current generation")
		}

		var target int
		if len(args) == 0 {
			if cur < 2 {
				return fmt.Errorf(
					"only one generation exists",
				)
			}
			target = cur - 1
		} else {
			target, err = strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf(
					"invalid generation number: %w", err,
				)
			}
			if target <= 0 {
				return fmt.Errorf(
					"generation number must be positive",
				)
			}
		}

		if dryRun {
			out.Info(fmt.Sprintf(
				"Would rollback to generation %d", target,
			))
			return nil
		}

		if err := generation.Rollback(
			galeDir, defaultStoreRoot(), target,
		); err != nil {
			return fmt.Errorf("rollback: %w", err)
		}

		out.Success(fmt.Sprintf(
			"Rolled back to generation %d", target,
		))
		return nil
	},
}

// pluralGeneration returns the noun matching a count of
// generations.
func pluralGeneration(n int) string {
	if n == 1 {
		return "generation"
	}
	return "generations"
}

// currentGenNumber returns the active generation's number from a
// listing, or 0 when no generation is current. Reads the flag
// generation.List already computed rather than re-reading the
// current symlink.
func currentGenNumber(gens []generation.GenInfo) int {
	for _, g := range gens {
		if g.Current {
			return g.Number
		}
	}
	return 0
}

// genMarker returns the one-column marker for a generation:
// "*" for the active one, "+" for a generation above it, " "
// for history below it.
//
// A generation above current exists only after a rollback, and
// it is retained on purpose: the number permanently identifies
// that snapshot (gh#189), so gc skips it and auto-prune's
// numeric cutoff cannot reach it until current climbs back past
// it. Rendering it like history below current hid the one state
// a user has to see before a later rebuild climbs
// past that branch (gh#206).
func genMarker(g generation.GenInfo, cur int) string {
	switch {
	case g.Current:
		return "*"
	case cur > 0 && g.Number > cur:
		return "+"
	default:
		return " "
	}
}

// resolveGenerationsGaleDir returns the .gale dir for the
// generations commands. Like which, it does not require
// gale.toml to exist — only the generation symlinks.
func resolveGenerationsGaleDir(global, project bool) (string, error) {
	galeDir, _, err := resolveScopedPaths(global, project)
	return galeDir, err
}

// addGenerationsScopeFlags registers -g/-p on the given
// generations subcommand using package-level state shared
// with the parent.
func addGenerationsScopeFlags(c *cobra.Command) {
	c.Flags().BoolVarP(&generationsGlobal, "global", "g", false,
		"Use the global generation dir")
	c.Flags().BoolVarP(&generationsProject, "project", "p", false,
		"Use the project generation dir")
}

func init() {
	addGenerationsScopeFlags(generationsCmd)
	addGenerationsScopeFlags(genRollbackCmd)
	generationsCmd.AddCommand(genRollbackCmd)
	rootCmd.AddCommand(generationsCmd)
}
