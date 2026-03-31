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

		var promptFile string
		if cfg, err := loadAppConfig(); err == nil {
			promptFile = cfg.Anthropic.PromptFile
		}

		outputDir := resolveRecipeOutputDir(createRecipeOutput)
		return runCreateRecipe(
			repo, client, promptFile, outputDir, out, 0)
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

// maxRecipeDepth limits recursive dependency creation.
const maxRecipeDepth = 3

// runCreateRecipe runs the recipe creation agent and
// handles recursive dependency resolution. If the agent
// reports a missing dependency, the dep is created first
// and the original recipe is retried.
func runCreateRecipe(
	repo string,
	client *ai.Client,
	promptFile string,
	outputDir string,
	out *output.Output,
	depth int,
) error {
	if depth > maxRecipeDepth {
		return fmt.Errorf(
			"dependency chain too deep (max %d)",
			maxRecipeDepth)
	}

	tmpDir, err := os.MkdirTemp("", "gale-recipe-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	out.Info(fmt.Sprintf("Creating recipe for %s...", repo))

	checker := buildRecipeChecker(outputDir)
	tools, cleanup := ai.RecipeTools(tmpDir, checker)
	defer cleanup()

	result, err := client.RunAgent(
		ai.RecipePrompt(promptFile),
		fmt.Sprintf(
			"Create a gale recipe for the GitHub repository %s. "+
				"Use the tools to fetch repo info, list files to detect the "+
				"build system, check that dependencies have recipes, "+
				"download the source tarball, compute its SHA256, "+
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

	// Check for missing dependency signal.
	if name, depRepo, ok := parseMissingDep(result); ok {
		if outputDir == "" {
			return fmt.Errorf(
				"dependency %q not found; use -o to specify "+
					"an output directory for recursive creation",
				name)
		}
		out.Info(fmt.Sprintf(
			"Dependency %q not found, creating from %s...",
			name, depRepo))
		if err := runCreateRecipe(
			depRepo, client, promptFile,
			outputDir, out, depth+1); err != nil {
			return fmt.Errorf("create dependency %s: %w",
				name, err)
		}
		// Retry the original recipe.
		return runCreateRecipe(
			repo, client, promptFile,
			outputDir, out, depth+1)
	}

	// Find the generated recipe file in tmpDir.
	recipePath := findRecipeFile(tmpDir)
	if recipePath == "" {
		out.Success("Recipe created")
		fmt.Fprintln(os.Stderr, result)
		return nil
	}

	if outputDir != "" {
		destPath, err := moveRecipe(recipePath, outputDir)
		if err != nil {
			return fmt.Errorf("writing recipe: %w", err)
		}
		out.Success(fmt.Sprintf("Recipe written to %s", destPath))
	} else {
		data, err := os.ReadFile(recipePath)
		if err != nil {
			return fmt.Errorf("reading recipe: %w", err)
		}
		fmt.Print(string(data))
		out.Success("Recipe printed to stdout")
	}
	return nil
}

// buildRecipeChecker returns a function that checks
// whether a gale recipe exists for a package name.
// Checks local output dir first (for deps created
// during recursion), then falls back to the registry.
func buildRecipeChecker(outputDir string) func(string) bool {
	reg := newRegistry()
	return func(name string) bool {
		// Check local output dir first.
		if outputDir != "" {
			letter := string(name[0])
			path := filepath.Join(
				outputDir, letter, name+".toml")
			if _, err := os.Stat(path); err == nil {
				return true
			}
		}
		// Fall back to registry.
		_, err := reg.FetchRecipe(name)
		return err == nil
	}
}

// parseMissingDep parses a MISSING_DEP response from the
// agent. Format: "MISSING_DEP <name> <owner/repo>".
// Returns the dep name, GitHub repo, and whether the
// response matched.
func parseMissingDep(s string) (name, repo string, ok bool) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "MISSING_DEP ") {
		return "", "", false
	}
	parts := strings.Fields(s)
	if len(parts) != 3 {
		return "", "", false
	}
	return parts[1], parts[2], true
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
