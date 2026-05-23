package main

import (
	"fmt"
	"sort"

	ver "github.com/kelp/gale/internal/version"
	"github.com/spf13/cobra"
)

var (
	outdatedRecipes   string
	outdatedNoRefresh bool
)

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

		// Auto-refresh configured taps so listings reflect the
		// actual upstream state. Skip with --recipes,
		// --no-refresh, or GALE_OFFLINE=1.
		if outdatedRecipes == "" && !tapsOfflineMode(outdatedNoRefresh) {
			if err := refreshConfiguredTapsDefault(out); err != nil {
				out.Warn(fmt.Sprintf("tap refresh: %v", err))
			}
		}

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

		// Sort package names before iterating cfg.Packages so
		// the output order is deterministic. Go's map iteration
		// is randomised; peer read-only commands (list, sbom,
		// env, inspect) all sort first.
		names := make([]string, 0, len(cfg.Packages))
		for name := range cfg.Packages {
			names = append(names, name)
		}
		sort.Strings(names)

		var items []outdatedItem
		for _, name := range names {
			version := cfg.Packages[name]
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
	outdatedCmd.Flags().BoolVar(&outdatedNoRefresh, "no-refresh", false,
		"Skip refreshing configured recipe taps before resolving")
	rootCmd.AddCommand(outdatedCmd)
}
