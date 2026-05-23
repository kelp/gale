package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/kelp/gale/internal/registry"
	"github.com/spf13/cobra"
)

var searchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search for packages",
	Long:  "Search for packages by name or description using fuzzy matching.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSearch(
			cmd.OutOrStdout(), newRegistry(), args[0])
	},
}

// runSearch executes a search against reg and writes matching
// results to stdout. Returns a non-nil error when the query is
// empty or no packages match — exit 1, consistent with peer
// not-found commands (info, which, verify, audit). Cobra
// surfaces the error as `Error: ...` on stderr and suppresses
// usage (see root.go), so the user gets a single notice on
// stderr with stdout empty.
func runSearch(
	stdout io.Writer,
	reg *registry.Registry,
	query string,
) error {
	if strings.TrimSpace(query) == "" {
		return fmt.Errorf("search query is empty")
	}

	results, err := reg.Search(query)
	if err != nil {
		return fmt.Errorf("searching: %w", err)
	}

	if len(results) == 0 {
		return fmt.Errorf(
			"no packages found matching %q", query)
	}

	for _, r := range results {
		fmt.Fprintf(stdout, "%-20s %s\n", r.Name, r.Description)
	}
	return nil
}

func init() {
	rootCmd.AddCommand(searchCmd)
}
