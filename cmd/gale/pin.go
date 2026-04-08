package main

import (
	"fmt"
	"os"

	"github.com/kelp/gale/internal/config"
	"github.com/spf13/cobra"
)

var pinCmd = &cobra.Command{
	Use:   "pin <package>",
	Short: "Pin a package to skip during updates",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		out := newCmdOutput(cmd)
		name := args[0]

		configPath, err := resolveConfigPath(false)
		if err != nil {
			return err
		}

		data, err := os.ReadFile(configPath)
		if err != nil {
			return fmt.Errorf("reading config: %w", err)
		}
		cfg, err := config.ParseGaleConfig(string(data))
		if err != nil {
			return err
		}
		if _, ok := cfg.Packages[name]; !ok {
			return fmt.Errorf(
				"%s is not in gale.toml", name)
		}

		if err := config.PinPackage(configPath, name); err != nil {
			return fmt.Errorf("pinning %s: %w", name, err)
		}

		out.Success(fmt.Sprintf("Pinned %s@%s",
			name, cfg.Packages[name]))
		return nil
	},
}

var unpinCmd = &cobra.Command{
	Use:   "unpin <package>",
	Short: "Unpin a package to allow updates",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		out := newCmdOutput(cmd)
		name := args[0]

		configPath, err := resolveConfigPath(false)
		if err != nil {
			return err
		}

		if err := config.UnpinPackage(
			configPath, name); err != nil {
			return fmt.Errorf("unpinning %s: %w", name, err)
		}

		out.Success(fmt.Sprintf("Unpinned %s", name))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(pinCmd)
	rootCmd.AddCommand(unpinCmd)
}
