package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/generation"
	"github.com/spf13/cobra"
)

var (
	generationsGlobal  bool
	generationsProject bool
)

var generationsCmd = &cobra.Command{
	Use:   "generations",
	Short: "List and manage generations",
	// ExactArgs(0): bare `gale generations` lists; the diff /
	// rollback children handle their own arg shapes. Falling
	// through to here with an unrecognised positional (e.g.
	// `gale generations nosuchcmd`) must reject it cleanly
	// rather than echo cobra's stock "unknown command" line.
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

		for _, g := range gens {
			marker := " "
			if g.Current {
				marker = "*"
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"%s %-3d %d packages\n",
				marker, g.Number, len(g.Packages))
		}

		return nil
	},
}

var genDiffCmd = &cobra.Command{
	Use:   "diff [from] [to]",
	Short: "Show differences between two generations",
	Args:  cobra.RangeArgs(0, 2),
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

		var from, to int
		switch len(args) {
		case 0:
			// No args: diff previous against current. Both
			// require Current() to be set.
			cur, err := generation.Current(galeDir)
			if err != nil {
				return fmt.Errorf("reading current: %w", err)
			}
			if cur == 0 {
				// Match the parent `gale generations`
				// empty-state: stderr notice, exit 0, no
				// stdout output. Subcommands of a group must
				// agree on what "nothing to do" looks like.
				fmt.Fprintln(cmd.ErrOrStderr(),
					"No generations found.")
				return nil
			}
			if cur < 2 {
				return fmt.Errorf(
					"only one generation exists",
				)
			}
			from = cur - 1
			to = cur
		case 1:
			// One arg: diff the named generation against
			// current. Current() must be set.
			cur, err := generation.Current(galeDir)
			if err != nil {
				return fmt.Errorf("reading current: %w", err)
			}
			if cur == 0 {
				return fmt.Errorf("no current generation")
			}
			from, err = strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf(
					"invalid generation number: %w", err,
				)
			}
			to = cur
		case 2:
			// Two args: explicit pair. Don't require Current()
			// — generation.Diff validates both ends exist.
			var err error
			from, err = strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf(
					"invalid from generation: %w", err,
				)
			}
			to, err = strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf(
					"invalid to generation: %w", err,
				)
			}
		}

		d, err := generation.Diff(
			galeDir, storeRoot, from, to,
		)
		if err != nil {
			return fmt.Errorf("diffing generations: %w", err)
		}

		out := cmd.OutOrStdout()
		for _, pkg := range d.Added {
			fmt.Fprintf(out, "+ %s\n", pkg)
		}
		for _, pkg := range d.Removed {
			fmt.Fprintf(out, "- %s\n", pkg)
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

// resolveGenerationsGaleDir returns the .gale dir for the
// generations commands. Like which, it does not require
// gale.toml to exist — only the generation symlinks.
func resolveGenerationsGaleDir(global, project bool) (string, error) {
	if global {
		return galeConfigDir()
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getting working dir: %w", err)
	}
	if project {
		projPath, err := config.FindGaleConfig(cwd)
		if err != nil {
			return "", fmt.Errorf(
				"no project found — run 'gale init' first",
			)
		}
		return galeDirForConfig(projPath)
	}
	// Auto. galeDirForConfig (not Dir(cfg)/.gale): under
	// ~/.gale the found config is the GLOBAL one and the
	// derived dir would be the bogus ~/.gale/.gale (gh#96).
	if projPath, err := config.FindGaleConfig(cwd); err == nil {
		return galeDirForConfig(projPath)
	}
	return galeConfigDir()
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
	addGenerationsScopeFlags(genDiffCmd)
	addGenerationsScopeFlags(genRollbackCmd)
	generationsCmd.AddCommand(genDiffCmd)
	generationsCmd.AddCommand(genRollbackCmd)
	rootCmd.AddCommand(generationsCmd)
}
