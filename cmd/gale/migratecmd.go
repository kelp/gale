package main

import (
	"github.com/spf13/cobra"
)

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Gone; use gale fetch or gale fetch-adopt",
	Long: "gale migrate poured bottles without fixup. That path is " +
		"gone. Use gale fetch for a new install, or gale fetch-adopt " +
		"to convert a v1 lock.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, err := newCmdContext("", true, false)
		if err != nil {
			return err
		}
		return runMigrate(ctx, newCmdOutput(cmd))
	},
}

func init() {
	rootCmd.AddCommand(migrateCmd)
}
