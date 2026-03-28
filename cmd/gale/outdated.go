package main

import (
	"fmt"
	"os"

	"github.com/kelp/gale/internal/output"
	"github.com/spf13/cobra"
)

var outdatedLocal bool

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
		out := output.New(os.Stderr, !cmd.Flags().Changed("no-color"))

		ctx, err := newCmdContext(outdatedLocal)
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

			if r.Package.Version != version {
				items = append(items, outdatedItem{
					Name:    name,
					Current: version,
					Latest:  r.Package.Version,
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
	outdatedCmd.Flags().BoolVar(&outdatedLocal, "local", false,
		"Resolve recipes from sibling gale-recipes directory")
	rootCmd.AddCommand(outdatedCmd)
}
