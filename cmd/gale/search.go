package main

import (
	"fmt"
	"os"

	"github.com/kelp/gale/internal/ai"
	"github.com/kelp/gale/internal/output"
	"github.com/spf13/cobra"
)

var searchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search for packages",
	Long:  "Search for packages by name or description using fuzzy matching.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		query := args[0]
		out := output.New(os.Stderr, !cmd.Flags().Changed("no-color"))

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

// loadAIClient creates an AI client from config.toml, or nil
// if no API key is configured.
func loadAIClient() *ai.Client {
	cfg, err := loadAppConfig()
	if err != nil {
		return nil
	}

	if cfg.AI.APIKey == "" {
		return nil
	}

	return ai.NewClient(cfg.AI.APIKey)
}
