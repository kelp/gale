package main

import (
	"fmt"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/store"
	"github.com/spf13/cobra"
)

var (
	removeGlobal  bool
	removeProject bool
)

var removeCmd = &cobra.Command{
	Use:   "remove <package>",
	Short: "Remove a package",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if removeGlobal && removeProject {
			return fmt.Errorf(
				"cannot use both --global and --project")
		}

		name := args[0]

		out := newCmdOutput(cmd)

		ctx, err := newCmdContext("", removeGlobal, removeProject)
		if err != nil {
			return err
		}

		st := store.NewStore(ctx.StoreRoot)

		// Look up the package in the resolved config.
		cfg, err := ctx.LoadConfig()
		if err != nil {
			return err
		}

		version, ok := cfg.Packages[name]
		if !ok {
			return fmt.Errorf(
				"%s is not in %s", name, ctx.GalePath)
		}

		if dryRun {
			out.Info(fmt.Sprintf(
				"remove %s@%s", name, version))
			return nil
		}

		// Update config first so a failed write does not
		// leave the store missing but config still listing
		// the package.
		if err := config.RemovePackage(
			ctx.GalePath, name); err != nil {
			return fmt.Errorf("removing from config: %w",
				err)
		}
		out.Info(fmt.Sprintf(
			"Removed %s from %s", name, ctx.GalePath))

		// Remove from lockfile. Warn on error but continue
		// since the main operation (config + store) succeeded.
		if err := ctx.RemoveLockEntry(name); err != nil {
			out.Warn(fmt.Sprintf(
				"Failed to update lockfile: %v", err))
		}

		// Rebuild the generation for this scope. Do this
		// before removing from the store so the generation
		// is updated with the new config (package removed)
		// before we delete the package from the store.
		if err := ctx.RebuildGeneration(); err != nil {
			return fmt.Errorf("rebuild generation: %w", err)
		}

		// Remove the declared version from the store.
		if st.IsInstalled(name, version) {
			if err := st.Remove(name, version); err != nil {
				return fmt.Errorf("removing from store: %w",
					err)
			}
			out.Info(fmt.Sprintf("Removed %s@%s from store",
				name, version))
		} else {
			out.Warn(fmt.Sprintf(
				"%s@%s not found in store", name, version))
		}

		out.Success(fmt.Sprintf("Removed %s", name))
		return nil
	},
}

func init() {
	removeCmd.Flags().BoolVarP(&removeGlobal, "global", "g",
		false, "Remove from global config")
	removeCmd.Flags().BoolVarP(&removeProject, "project", "p",
		false, "Remove from project config")
	rootCmd.AddCommand(removeCmd)
}
