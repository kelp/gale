package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kelp/gale/internal/ai"
	"github.com/kelp/gale/internal/output"
	"github.com/spf13/cobra"
)

var createRecipeOutput string

var createRecipeCmd = &cobra.Command{
	Use:   "create-recipe <repo>",
	Short: "Generate a recipe from a GitHub repo",
	Long: `Analyze a GitHub repository and generate a working recipe TOML file.
Accepts owner/repo, github.com/owner/repo, or a full HTTPS URL.
Requires an API key in ~/.gale/config.toml.

The agent fetches repo metadata, downloads the source tarball,
detects the build system, generates a recipe, lints it, and
iterates until the recipe is valid.

When run inside a gale-recipes directory, writes to
recipes/<letter>/<name>.toml. Otherwise prints the recipe
to stdout. Use -o <dir> to specify an output directory.`,
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

		// Always build in a temp dir (agent needs files
		// for lint). Move or print after success.
		tmpDir, err := os.MkdirTemp("", "gale-recipe-*")
		if err != nil {
			return fmt.Errorf("create temp dir: %w", err)
		}
		defer os.RemoveAll(tmpDir)

		out.Info(fmt.Sprintf("Creating recipe for %s...", repo))

		tools, cleanup := ai.RecipeTools(tmpDir)
		defer cleanup()

		result, err := client.RunAgent(
			ai.RecipePrompt(promptFile),
			fmt.Sprintf(
				"Create a gale recipe for the GitHub repository %s. "+
					"Use the tools to fetch repo info, list files to detect the "+
					"build system, download the source tarball, compute its SHA256, "+
					"write the recipe, and lint it. "+
					"When done, respond with ONLY the path to the recipe "+
					"file, nothing else.",
				repo),
			tools,
			15,
		)
		if err != nil {
			return fmt.Errorf("recipe generation: %w", err)
		}

		// Find the generated recipe file in tmpDir.
		recipePath := findRecipeFile(tmpDir)
		if recipePath == "" {
			out.Success("Recipe created")
			fmt.Fprintln(os.Stderr, result)
			return nil
		}

		// Determine output: -o flag, auto-detect, or stdout.
		outputDir := resolveRecipeOutputDir(createRecipeOutput)
		if outputDir != "" {
			destPath, err := moveRecipe(recipePath, outputDir)
			if err != nil {
				return fmt.Errorf("writing recipe: %w", err)
			}
			out.Success(fmt.Sprintf("Recipe written to %s", destPath))
		} else {
			// Print to stdout.
			data, err := os.ReadFile(recipePath)
			if err != nil {
				return fmt.Errorf("reading recipe: %w", err)
			}
			fmt.Print(string(data))
			out.Success("Recipe printed to stdout")
		}
		return nil
	},
}

func init() {
	createRecipeCmd.Flags().StringVarP(&createRecipeOutput,
		"output", "o", "",
		"Output directory for recipe (default: auto-detect)")
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

// resolveRecipeOutputDir determines where to write
// recipes. If explicit is set, use it. Otherwise
// detect if we're in a gale-recipes directory and
// use its recipes/ subdir. Returns empty string for
// stdout mode.
func resolveRecipeOutputDir(explicit string) string {
	if explicit != "" {
		return explicit
	}

	// Check if cwd is inside a gale-recipes repo.
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}

	// Walk up looking for a recipes/ directory with
	// letter-bucketed structure.
	dir := cwd
	for {
		candidate := filepath.Join(dir, "recipes")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			if _, err := os.Stat(filepath.Join(candidate, "j")); err == nil {
				return candidate
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return ""
}

// findRecipeFile finds the first .toml file in a
// letter-bucketed temp directory.
func findRecipeFile(dir string) string {
	var found string
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if filepath.Ext(path) == ".toml" {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// moveRecipe copies a recipe file to the output dir,
// preserving letter-bucketed naming.
func moveRecipe(src, outputDir string) (string, error) {
	data, err := os.ReadFile(src)
	if err != nil {
		return "", err
	}

	base := filepath.Base(src)
	name := strings.TrimSuffix(base, ".toml")
	letter := string(name[0])
	destDir := filepath.Join(outputDir, letter)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}

	dest := filepath.Join(destDir, base)
	if err := os.WriteFile(dest, data, 0o644); err != nil { //nolint:gosec
		return "", err
	}
	return dest, nil
}
