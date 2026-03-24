package main

import (
	"fmt"
	"os"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/output"
	"github.com/spf13/cobra"
)

var removeCmd = &cobra.Command{
	Use:   "remove <package>",
	Short: "Remove a package",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		out := output.New(os.Stderr, !cmd.Flags().Changed("no-color"))

		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working dir: %w", err)
		}

		configPath, err := config.FindGaleConfig(cwd)
		if err != nil {
			return fmt.Errorf("no gale.toml found: %w", err)
		}

		if err := config.RemovePackage(configPath, name); err != nil {
			return fmt.Errorf("removing package: %w", err)
		}

		out.Success(fmt.Sprintf("Removed %s from %s",
			name, configPath))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(removeCmd)
}
