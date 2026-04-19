package main

import (
	"fmt"

	ver "github.com/kelp/gale/internal/version"
	"github.com/spf13/cobra"
)

var outdatedRecipes string

// outdatedItem represents a package with a newer version.
type outdatedItem struct {
	Name    string
	Current string
	Latest  string
}

var outdatedCmd = &cobra.Command{
	Use:   "outdated",
	Short: "Show packages with newer versions available",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		out := newCmdOutput(cmd)

		ctx, err := newCmdContext(outdatedRecipes, false, false)
		if err != nil {
			return err
		}

		cfg, err := ctx.LoadConfig()
		if err != nil {
			return err
		}

		if len(cfg.Packages) == 0 {
			out.Info("No packages installed.")
			return nil
		}

		var items []outdatedItem
		for name, version := range cfg.Packages {
			r, err := ctx.Resolver(name)
			if err != nil {
				out.Warn(fmt.Sprintf(
					"Skipping %s: %v", name, err))
				continue
			}

			// Compare via Full() so a revision bump (e.g.
			// recipe revision 1 → 2 with unchanged upstream
			// version) still shows as outdated. Raw Package.Version
			// only carries the upstream triple, which drops the
			// revision entirely.
			latest := r.Package.Full()
			if ver.IsNewer(latest, version) {
				items = append(items, outdatedItem{
					Name:    name,
					Current: version,
					Latest:  latest,
				})
			}
		}

		if len(items) == 0 {
			out.Success("Everything is up to date.")
			return nil
		}

		for _, line := range formatOutdated(items) {
			fmt.Println(line)
		}

		return nil
	},
}

// formatOutdated formats outdated items as lines of text.
func formatOutdated(items []outdatedItem) []string {
	lines := make([]string, len(items))
	for i, item := range items {
		lines[i] = fmt.Sprintf("%s %s → %s",
			item.Name, item.Current, item.Latest)
	}
	return lines
}

func init() {
	outdatedCmd.Flags().StringVar(&outdatedRecipes, "recipes", "",
		"Use local recipes directory (default: ../gale-recipes/)")
	outdatedCmd.Flags().Lookup("recipes").NoOptDefVal = "auto"
	rootCmd.AddCommand(outdatedCmd)
}
