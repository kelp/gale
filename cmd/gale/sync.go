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

		var installed, failed int
		for name, version := range cfg.Packages {
			// Check store first — pinned version present?
			if ctx.Installer.Store.IsInstalled(name, version) {
				out.Info(fmt.Sprintf(
					"%s@%s up to date", name, version))
				continue
			}

			// Not in store — fetch recipe, check version.
			r, err := ctx.Resolver(name)
			if err != nil {
				out.Warn(fmt.Sprintf(
					"Failed to resolve %s: %v", name, err))
				failed++
				continue
			}

			if r.Package.Version != version {
				// Latest doesn't match pin. Try versioned
				// fetch from the registry index.
				if ctx.Registry != nil {
					pinned, vErr := ctx.Registry.FetchRecipeVersion(
						name, version)
					if vErr == nil {
						r = pinned
					}
				}

				// Still doesn't match after versioned fetch.
				if r.Package.Version != version {
					out.Warn(fmt.Sprintf(
						"%s@%s not in store (registry has %s). "+
							"Run 'gale update %s' to install latest.",
						name, version, r.Package.Version, name))
					failed++
					continue
				}
			}

			// Versions match — install.
			out.Info(fmt.Sprintf("Installing %s@%s...",
				name, version))

			result, err := ctx.Installer.Install(r)
			if err != nil {
				out.Warn(fmt.Sprintf(
					"Failed to install %s: %v", name, err))
				failed++
				continue
			}

			reportResult(out, result, "Installed", "built from source")
			installed++
		}

		if err := rebuildGeneration(ctx.GaleDir,
			ctx.StoreRoot, ctx.GalePath); err != nil {
			return fmt.Errorf("rebuild generation: %w", err)
		}

		if failed > 0 {
			out.Warn(fmt.Sprintf(
				"Sync finished with %d error(s)", failed))
			return fmt.Errorf(
				"%d package(s) could not be synced", failed)
		}

		out.Success(fmt.Sprintf(
			"Sync complete: %d installed, %d up to date",
			installed,
			len(cfg.Packages)-installed))
		return nil
	},
}

func init() {
	syncCmd.Flags().BoolVar(&syncLocal, "local", false,
		"Resolve recipes from sibling gale-recipes directory")
	rootCmd.AddCommand(syncCmd)
}
