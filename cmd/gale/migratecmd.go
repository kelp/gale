package main

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/kelp/gale/internal/index"
)

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Convert a v1 lock to a fetch lock",
	Long: "Resolve every default-target root against one index " +
		"commit, print a lock diff, and after confirmation stage " +
		"fetch trees, write a v2 lock, and swap current last. " +
		"Migrates a v1 lock. Not a second installer: gale install " +
		"already fetches.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateScopeFlags(adoptGlobal, adoptProject); err != nil {
			return err
		}
		c, err := newCmdContext("", adoptGlobal, adoptProject)
		if err != nil {
			return err
		}
		src := index.Source{}
		if adoptIndex != "" {
			src.Dir = adoptIndex
		}
		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		return runFetchAdopt(ctx, c, adoptReq{
			Source: src,
			Yes:    adoptYes,
			DryRun: dryRun,
			TTY:    adoptTTY(),
			In:     cmd.InOrStdin(),
			Out:    cmd.OutOrStdout(),
			Err:    cmd.ErrOrStderr(),
		})
	},
}

func init() {
	migrateCmd.Flags().BoolVarP(&adoptGlobal, "global", "g",
		false, "Adopt the global config")
	migrateCmd.Flags().BoolVarP(&adoptProject, "project", "p",
		false, "Adopt the project config")
	migrateCmd.Flags().BoolVar(&adoptYes, "yes",
		false, "Skip the confirmation prompt")
	migrateCmd.Flags().StringVar(&adoptIndex, "index",
		"", "Resolve against a local index checkout")
	rootCmd.AddCommand(migrateCmd)
}
