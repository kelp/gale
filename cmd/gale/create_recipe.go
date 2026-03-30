package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/kelp/gale/internal/ai"
	"github.com/kelp/gale/internal/output"
	"github.com/spf13/cobra"
)

var createRecipeCmd = &cobra.Command{
	Use:   "create-recipe <repo>",
	Short: "Generate a recipe from a GitHub repo",
	Long: `Analyze a GitHub repository and generate a working recipe TOML file.
Accepts owner/repo, github.com/owner/repo, or a full HTTPS URL.
Requires an API key in ~/.gale/config.toml.

The agent fetches repo metadata, downloads the source tarball,
detects the build system, generates a recipe, lints it, and
iterates until the recipe is valid.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		repo := normalizeRepo(args[0])
		out := output.New(os.Stderr, !cmd.Flags().Changed("no-color"))

		client := loadAIClient()
		if client == nil {
			return fmt.Errorf(
				"create-recipe requires an AI API key in " +
					"~/.gale/config.toml")
		}

		// Load prompt extension from config.
		var promptFile string
		if cfg, err := loadAppConfig(); err == nil {
			promptFile = cfg.Anthropic.PromptFile
		}

		out.Info(fmt.Sprintf("Creating recipe for %s...", repo))

		tools, cleanup := ai.RecipeTools()
		defer cleanup()

		result, err := client.RunAgent(
			ai.RecipePrompt(promptFile),
			fmt.Sprintf(
				"Create a gale recipe for the GitHub repository %s. "+
					"Use the tools to fetch repo info, download the source "+
					"tarball, compute its SHA256, detect the build system, "+
					"write the recipe, and lint it. "+
					"When done, respond with the path to the recipe file.",
				repo),
			tools,
			10,
		)
		if err != nil {
			return fmt.Errorf("recipe generation: %w", err)
		}

		out.Success("Recipe created")
		fmt.Fprintln(os.Stderr, result)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(createRecipeCmd)
}

// normalizeRepo extracts owner/repo from various
// GitHub URL formats.
func normalizeRepo(input string) string {
	input = strings.TrimSuffix(input, "/")
	input = strings.TrimSuffix(input, ".git")
	input = strings.TrimPrefix(input, "https://github.com/")
	input = strings.TrimPrefix(input, "http://github.com/")
	input = strings.TrimPrefix(input, "github.com/")
	return input
}
