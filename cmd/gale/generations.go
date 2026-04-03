package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/output"
	"github.com/spf13/cobra"
)

var generationsCmd = &cobra.Command{
	Use:   "generations",
	Short: "List and manage generations",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		galeDir, err := resolveGaleDir()
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
		galeDir, err := resolveGaleDir()
		if err != nil {
			return err
		}

		storeRoot := defaultStoreRoot()

		cur, err := generation.Current(galeDir)
		if err != nil {
			return fmt.Errorf("reading current: %w", err)
		}
		if cur == 0 {
			return fmt.Errorf("no current generation")
		}

		var from, to int
		switch len(args) {
		case 0:
			if cur < 2 {
				return fmt.Errorf(
					"only one generation exists")
			}
			from = cur - 1
			to = cur
		case 1:
			from, err = strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf(
					"invalid generation number: %w", err)
			}
			to = cur
		case 2:
			from, err = strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf(
					"invalid from generation: %w", err)
			}
			to, err = strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf(
					"invalid to generation: %w", err)
			}
		}

		d, err := generation.Diff(
			galeDir, storeRoot, from, to)
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
		galeDir, err := resolveGaleDir()
		if err != nil {
			return err
		}

		out := output.New(os.Stderr,
			!cmd.Flags().Changed("no-color"))

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
					"only one generation exists")
			}
			target = cur - 1
		} else {
			target, err = strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf(
					"invalid generation number: %w", err)
			}
		}

		if dryRun {
			out.Info(fmt.Sprintf(
				"Would rollback to generation %d", target))
			return nil
		}

		if err := generation.Rollback(
			galeDir, target); err != nil {
			return fmt.Errorf("rollback: %w", err)
		}

		out.Success(fmt.Sprintf(
			"Rolled back to generation %d", target))
		return nil
	},
}

func init() {
	generationsCmd.AddCommand(genDiffCmd)
	generationsCmd.AddCommand(genRollbackCmd)
	rootCmd.AddCommand(generationsCmd)
}
