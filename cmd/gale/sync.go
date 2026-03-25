package main

import (
	"fmt"
	"os"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/output"
	"github.com/spf13/cobra"
)

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Install everything declared in gale.toml",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		out := output.New(os.Stderr, !cmd.Flags().Changed("no-color"))

		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working dir: %w", err)
		}

		galePath, err := config.FindGaleConfig(cwd)
		if err != nil {
			return fmt.Errorf("no gale.toml found: %w", err)
		}

		data, err := os.ReadFile(galePath)
		if err != nil {
			return fmt.Errorf("reading config: %w", err)
		}

		cfg, err := config.ParseGaleConfig(string(data))
		if err != nil {
			return fmt.Errorf("parsing config: %w", err)
		}

		if len(cfg.Packages) == 0 {
			out.Info("No packages to sync.")
			return nil
		}

		// For now, just report what would be synced.
		// Full install flow requires recipe repo + download.
		for name, version := range cfg.Packages {
			out.Info(fmt.Sprintf("Would sync %s@%s", name, version))
		}

		out.Success("Sync complete (dry run — install flow pending)")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(syncCmd)
}
