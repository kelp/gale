package main

import (
	"github.com/spf13/cobra"
)

var migrateRecipes string

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Refetch and replace store directories that attest nothing",
	Long: "Converge every package installed before gale enforced " +
		"gale.lock.\n\n" +
		"Such a package has no provenance record, so nothing can prove " +
		"which bytes it holds, and a locked environment refuses to " +
		"activate it. For each one whose recipe declares a prebuilt " +
		"binary, migrate refetches the artifact, verifies it, and puts " +
		"the verified result in place of the old directory. It never " +
		"writes a record beside a directory it did not replace.\n\n" +
		"The whole machine is one unit: every scope is consulted about " +
		"every candidate before anything is replaced, because the store " +
		"is shared and a per-scope pass would race its neighbours. " +
		"Source-built packages cannot be migrated by refetching, so " +
		"they are listed rather than claimed.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Global scope deliberately, and not because migrate belongs to
		// it: the store and the project registry are machine-wide, and
		// resolving a project context would make the command's reach
		// depend on the directory it was run from.
		ctx, err := newCmdContext(migrateRecipes, true, false)
		if err != nil {
			return err
		}
		return runMigrate(ctx, newCmdOutput(cmd))
	},
}

func init() {
	migrateCmd.Flags().StringVar(&migrateRecipes, "recipes", "",
		"Resolve recipes from a local directory instead of the registry")
	rootCmd.AddCommand(migrateCmd)
}
