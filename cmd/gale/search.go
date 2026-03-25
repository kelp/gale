package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/kelp/gale/internal/ai"
	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/repo"
	"github.com/spf13/cobra"
)

var searchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search for packages",
	Long:  "Search for packages by name. Uses AI-enhanced search if an API key is configured.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		query := args[0]
		out := output.New(os.Stderr, !cmd.Flags().Changed("no-color"))

		galeDir, err := galeConfigDir()
		if err != nil {
			return err
		}

		// Try AI-enhanced search first.
		client := loadAIClient(galeDir)
		if client != nil {
			result, err := client.Complete(
				fmt.Sprintf("List CLI tool package names matching: %s. Return only names, one per line.", query))
			if err == nil {
				out.Info("AI-enhanced search results:")
				fmt.Println(result)
				return nil
			}
			// Fall through to local search on AI error.
		}

		// Local substring search across repos.
		cacheRoot := filepath.Join(galeDir, "repos")
		mgr := repo.NewManager(cacheRoot)

		results, err := mgr.Search(query)
		if err != nil {
			return fmt.Errorf("searching repos: %w", err)
		}

		if len(results) == 0 {
			out.Warn(fmt.Sprintf("No packages found matching %q", query))
			return nil
		}

		for _, r := range results {
			fmt.Printf("%s (%s)\n", r.Package, r.RepoName)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(searchCmd)
}

// loadAIClient creates an AI client from config.toml, or nil
// if no API key is configured.
func loadAIClient(galeDir string) *ai.Client {
	configPath := filepath.Join(galeDir, "config.toml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil
	}

	cfg, err := config.ParseAppConfig(string(data))
	if err != nil {
		return nil
	}

	if cfg.AI.APIKey == "" {
		return nil
	}

	return ai.NewClient(cfg.AI.APIKey)
}
