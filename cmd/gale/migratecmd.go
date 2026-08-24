package main

import (
	"github.com/spf13/cobra"
)

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Gone; use gale install or gale fetch-adopt",
	Long: "gale migrate poured bottles without fixup. That path is " +
		"gone. Use gale install for a new install, or gale fetch-adopt " +
		"to convert a v1 lock.",
	Args: cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		return runMigrate()
	},
}

func init() {
	rootCmd.AddCommand(migrateCmd)
}
