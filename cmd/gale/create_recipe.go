package main

import (
	"fmt"
	"os"

	"github.com/kelp/gale/internal/output"
	"github.com/spf13/cobra"
)

var createRecipeCmd = &cobra.Command{
	Use:   "create-recipe <github-url>",
	Short: "Generate a recipe from a GitHub repo",
	Long:  "Analyze a GitHub repository and generate a recipe TOML file. Requires an API key in ~/.gale/config.toml.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		repoURL := args[0]
		out := output.New(os.Stderr, !cmd.Flags().Changed("no-color"))

		client := loadAIClient()
		if client == nil {
			return fmt.Errorf("create-recipe requires an AI API key in ~/.gale/config.toml")
		}

		out.Info(fmt.Sprintf("Analyzing %s...", repoURL))

		prompt := fmt.Sprintf(`Analyze the GitHub repository at %s and generate a gale recipe TOML file.

The recipe format is:
[package]
name = "..."
version = "..."
description = "..."
license = "..."
homepage = "..."

[source]
url = "..."
sha256 = "..."

[build]
system = "..."
steps = [...]

[dependencies]
build = [...]
runtime = [...]

Output only the TOML content.`, repoURL)

		result, err := client.Complete(prompt)
		if err != nil {
			return fmt.Errorf("AI recipe generation: %w", err)
		}

		fmt.Println(result)
		out.Success("Recipe generated")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(createRecipeCmd)
}
