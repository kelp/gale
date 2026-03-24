package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/kelp/gale/internal/config"
	"github.com/spf13/cobra"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List installed packages",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working dir: %w", err)
		}

		// Try project config first, then global.
		configPath, err := config.FindGaleConfig(cwd)
		if err != nil {
			home, homeErr := os.UserHomeDir()
			if homeErr != nil {
				return fmt.Errorf("finding home dir: %w", homeErr)
			}
			configPath = filepath.Join(home, ".gale", "gale.toml")
		}

		data, err := os.ReadFile(configPath)
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Println("No packages installed.")
				return nil
			}
			return fmt.Errorf("reading config: %w", err)
		}

		cfg, err := config.ParseGaleConfig(string(data))
		if err != nil {
			return fmt.Errorf("parsing config: %w", err)
		}

		if len(cfg.Packages) == 0 {
			fmt.Println("No packages installed.")
			return nil
		}

		for name, version := range cfg.Packages {
			fmt.Printf("%s@%s\n", name, version)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(listCmd)
}
