package main

import (
	"fmt"
	"os"

	"github.com/kelp/gale/internal/output"
	"github.com/spf13/cobra"
)

var diffLocal bool

var diffCmd = &cobra.Command{
	Use:   "diff",
	Short: "Show what sync would change",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		out := output.New(os.Stderr, !cmd.Flags().Changed("no-color"))

		ctx, err := newCmdContext(diffLocal)
		if err != nil {
			return err
		}

		cfg, err := ctx.LoadConfig()
		if err != nil {
			return err
		}

		if len(cfg.Packages) == 0 {
			out.Info("No packages in gale.toml.")
			return nil
		}

		var needsInstall, upToDate, mismatched int
		for name, version := range cfg.Packages {
			if ctx.Installer.Store.IsInstalled(name, version) {
				upToDate++
				continue
			}

			// Check if registry has the pinned version.
			r, err := ctx.Resolver(name)
			if err != nil {
				fmt.Printf("  ? %s@%s (cannot resolve)\n",
					name, version)
				mismatched++
				continue
			}

			if r.Package.Version != version {
				fmt.Printf("  ! %s@%s (registry has %s)\n",
					name, version, r.Package.Version)
				mismatched++
			} else {
				fmt.Printf("  + %s@%s (will install)\n",
					name, version)
				needsInstall++
			}
		}

		if needsInstall == 0 && mismatched == 0 {
			out.Success("Nothing to do — all packages installed.")
		} else {
			fmt.Fprintf(os.Stderr, "\n")
			out.Info(fmt.Sprintf(
				"%d up to date, %d to install, %d version mismatch",
				upToDate, needsInstall, mismatched))
		}

		return nil
	},
}

func init() {
	diffCmd.Flags().BoolVar(&diffLocal, "local", false,
		"Resolve recipes from sibling gale-recipes directory")
	rootCmd.AddCommand(diffCmd)
}
