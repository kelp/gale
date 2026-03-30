package ai

import (
	_ "embed"
	"os"
	"path/filepath"
	"strings"
)

//go:embed prompts/create-recipe.md
var createRecipePrompt string

// RecipePrompt returns the system prompt for recipe
// creation. If promptFile is non-empty and the file
// exists, its contents are appended to the base prompt.
func RecipePrompt(promptFile string) string {
	base := strings.TrimSpace(createRecipePrompt)

	if promptFile == "" {
		return base
	}

	// Expand ~ to home directory.
	if strings.HasPrefix(promptFile, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			promptFile = filepath.Join(home, promptFile[2:])
		}
	}

	data, err := os.ReadFile(promptFile)
	if err != nil {
		return base
	}

	extra := strings.TrimSpace(string(data))
	if extra == "" {
		return base
	}

	return base + "\n\n## Additional instructions\n\n" + extra
}
