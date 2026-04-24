package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var searchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search for packages",
	Long:  "Search for packages by name or description using fuzzy matching.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		query := args[0]
		out := newCmdOutput(cmd)

		reg := newRegistry()
		results, err := reg.Search(query)
		if err != nil {
			return fmt.Errorf("searching: %w", err)
		}

		if len(results) == 0 {
			out.Warn(fmt.Sprintf(
				"No packages found matching %q", query))
			return nil
		}

		for _, r := range results {
			fmt.Printf("%-20s %s\n", r.Name, r.Description)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(searchCmd)
}
