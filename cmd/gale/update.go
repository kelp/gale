package main

import (
	"fmt"
	"os"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/output"
	"github.com/spf13/cobra"
)

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update all packages to the latest version",
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

		// For now, pin "latest" to "latest" — real resolution
		// will come when we have recipe repository support.
		lf := &lockfile.LockFile{
			Packages: make(map[string]string),
		}
		for name, version := range cfg.Packages {
			lf.Packages[name] = version
		}

		lockPath := galePath[:len(galePath)-len("gale.toml")] + "gale.lock"
		if err := lockfile.Write(lockPath, lf); err != nil {
			return fmt.Errorf("writing lock file: %w", err)
		}

		out.Success(fmt.Sprintf("Updated %s", lockPath))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(updateCmd)
}
