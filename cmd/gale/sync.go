package main

import (
	"fmt"
	"os"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/output"
	"github.com/spf13/cobra"
)

var syncLocal bool

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Install all packages in gale.toml",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		out := output.New(os.Stderr, !cmd.Flags().Changed("no-color"))

		ctx, err := newCmdContext(syncLocal)
		if err != nil {
			return err
		}

		data, err := os.ReadFile(ctx.GalePath)
		if err != nil {
			return fmt.Errorf("reading %s: %w", ctx.GalePath, err)
		}

		cfg, err := config.ParseGaleConfig(string(data))
		if err != nil {
			return fmt.Errorf("parsing config: %w", err)
		}

		if len(cfg.Packages) == 0 {
			out.Info("No packages to sync.")
			return nil
		}

		var installed int
		for name := range cfg.Packages {
			result, err := ctx.installPackage(name, out)
			if err != nil {
				out.Warn(fmt.Sprintf(
					"Failed to install %s: %v", name, err))
				continue
			}

			switch result.Method {
			case "cached":
				out.Info(fmt.Sprintf(
					"%s@%s already installed",
					result.Name, result.Version))
			case "binary":
				out.Success(fmt.Sprintf(
					"Installed %s@%s from binary",
					result.Name, result.Version))
				installed++
			case "source":
				out.Success(fmt.Sprintf(
					"Installed %s@%s (built from source)",
					result.Name, result.Version))
				installed++
			}
		}

		if err := rebuildGeneration(ctx.GaleDir,
			ctx.StoreRoot, ctx.GalePath); err != nil {
			return fmt.Errorf("rebuild generation: %w", err)
		}

		out.Success(fmt.Sprintf(
			"Sync complete: %d packages installed", installed))
		return nil
	},
}

func init() {
	syncCmd.Flags().BoolVar(&syncLocal, "local", false,
		"Resolve recipes from sibling gale-recipes directory")
	rootCmd.AddCommand(syncCmd)
}
