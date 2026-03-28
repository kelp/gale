package main

import (
	"fmt"
	"os"

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

		cfg, err := ctx.LoadConfig()
		if err != nil {
			return err
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

			reportResult(out, result, "Installed", "built from source")
			if result.Method != "cached" {
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
