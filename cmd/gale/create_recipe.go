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

		out.Info(fmt.Sprintf("Creating recipe for %s...", repo))

		tools, cleanup := ai.RecipeTools()
		defer cleanup()

		result, err := client.RunAgent(
			recipeSystemPrompt,
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

const recipeSystemPrompt = `You are a recipe generator for gale, a package manager for developer CLI tools.

Your job: given a GitHub repository, produce a working recipe TOML file.

## Recipe format

` + "```" + `toml
[package]
name = "toolname"
version = "1.0.0"
description = "Short description"
license = "MIT"
homepage = "https://..."

[source]
repo = "owner/toolname"
url = "https://github.com/owner/toolname/archive/refs/tags/v1.0.0.tar.gz"
sha256 = "actual-sha256-hash"
released_at = "2025-01-15"

[build]
system = "go"
steps = [
  "mkdir -p ${PREFIX}/bin",
  "go build -o ${PREFIX}/bin/toolname .",
]

[dependencies]
build = ["go"]
` + "```" + `

## Build variables

- ${PREFIX} — install destination directory
- ${JOBS} — CPU count for parallel make
- ${VERSION} — package version

## Build patterns

**Autotools** (configure/make):
` + "```" + `
steps = [
  "./configure --prefix=${PREFIX} --disable-docs",
  "make -j${JOBS}",
  "make install",
]
` + "```" + `

**Go**:
` + "```" + `
[dependencies]
build = ["go"]

[build]
system = "go"
steps = [
  "mkdir -p ${PREFIX}/bin",
  "go build -o ${PREFIX}/bin/toolname .",
]
` + "```" + `

**Cargo** (Rust):
` + "```" + `
[dependencies]
build = ["rust"]

[build]
system = "cargo"
steps = [
  "cargo install --path . --root ${PREFIX}",
]
` + "```" + `
The --path flag is required — without it cargo fetches from crates.io.

**CMake**:
` + "```" + `
[dependencies]
build = ["cmake"]

[build]
system = "cmake"
steps = [
  "cmake -B build -DCMAKE_INSTALL_PREFIX=${PREFIX}",
  "cmake --build build -j${JOBS}",
  "cmake --install build",
]
` + "```" + `

## Workflow

1. Call github_info to get repo metadata (description, license, latest release).
2. Call read_file to check for build system files: configure.ac, CMakeLists.txt, Cargo.toml, go.mod, Makefile, meson.build.
3. Call download_and_hash with the source tarball URL to get the real SHA256.
   - Prefer archive/refs/tags/TAG.tar.gz URLs over releases/download URLs for GitHub repos.
   - Strip the leading "v" from tag names when constructing the version field.
4. Call write_recipe with the generated TOML.
5. Call lint_recipe to validate. Fix any errors and rewrite.
6. Respond with the recipe file path.

## Rules

- Always compute the real SHA256 with download_and_hash. Never guess or placeholder.
- Use the source.repo field (owner/repo format) for auto-update support.
- Set released_at to today's date in YYYY-MM-DD format.
- Prefer static linking for CLI tools.
- List build dependencies in [dependencies.build].
- The build.system field should match the build system: "go", "cargo", "cmake", "autotools", or omit for plain make.
`
