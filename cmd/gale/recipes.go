package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/registry"
)

const localGHCRBase = "kelp/gale-recipes"

// loadRecipeFile reads and parses a recipe TOML file.
// When local is true, uses ParseLocal (skips binary
// section validation). Otherwise uses Parse.
func loadRecipeFile(path string, local bool) (*recipe.Recipe, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading recipe %s: %w", path, err)
	}
	if local {
		return recipe.ParseLocal(string(data))
	}
	return recipe.Parse(string(data))
}

// resolveRecipeResolver constructs a RecipeResolver from
// the --recipes flag value. When recipesFlag is non-empty,
// returns a local resolver for that directory. Otherwise
// composes a chain: configured `[[repos]]` taps (priority
// order, lowest number first) → registry. The returned
// registry is nil when using local recipes; when taps are
// wired in it remains non-nil so versioned fetches still
// resolve through the registry.
func resolveRecipeResolver(recipesFlag string) (installer.RecipeResolver, *registry.Registry, error) {
	if recipesFlag != "" {
		recipesDir, err := findLocalRecipesDir(recipesFlag)
		if err != nil {
			return nil, nil, err
		}
		return localRecipeResolver(recipesDir), nil, nil
	}

	repoResolvers, err := configuredRepoResolvers()
	if err != nil {
		return nil, nil, err
	}

	reg := newRegistry()
	if len(repoResolvers) == 0 {
		return reg.FetchRecipe, reg, nil
	}

	chain := make([]installer.RecipeResolver, 0, len(repoResolvers)+1)
	chain = append(chain, repoResolvers...)
	chain = append(chain, reg.FetchRecipe)
	return composeResolvers(chain...), reg, nil
}

// localRecipeResolver returns a RecipeResolver that reads
// recipes from a local recipes directory using letter-bucketed
// layout: <recipesDir>/<letter>/<name>.toml.
func localRecipeResolver(recipesDir string) installer.RecipeResolver {
	return func(name string) (*recipe.Recipe, error) {
		if name == "" {
			return nil, fmt.Errorf("empty package name")
		}
		letter := string(name[0])
		path := filepath.Join(recipesDir, letter, name+".toml")
		data, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// A genuine miss — composeResolvers falls
				// through to the next resolver in the chain.
				return nil, fmt.Errorf(
					"no local recipe for %q", name,
				)
			}
			// The recipe exists but cannot be read — a real
			// failure that must stop the resolver chain (gh#71).
			return nil, &recipeResolveError{
				fmt.Errorf("reading recipe %s: %w", path, err),
			}
		}
		rec, err := recipe.Parse(string(data))
		if err != nil {
			// Corrupt recipe — surface the parse error naming
			// the file instead of letting a lower-priority
			// resolver shadow it (gh#71).
			return nil, &recipeResolveError{
				fmt.Errorf("parsing recipe %s: %w", path, err),
			}
		}

		// If recipe has no inline binaries, try the
		// separate .binaries.toml file.
		if len(rec.Binary) == 0 {
			binPath := filepath.Join(
				recipesDir, letter, name+".binaries.toml",
			)
			binData, readErr := os.ReadFile(binPath)
			if readErr == nil {
				idx, parseErr := recipe.ParseBinaryIndex(
					string(binData),
				)
				if parseErr == nil {
					recipe.MergeBinaries(
						rec, idx, localGHCRBase,
					)
				}
			}
		}

		return rec, nil
	}
}

// findLocalRecipesDir resolves the --recipes override to a
// recipes directory (using its recipes/ subdirectory if
// present). The override is required: the bare --recipes form
// and its sibling ../gale-recipes/ fallback were removed
// (gh#114).
func findLocalRecipesDir(override string) (string, error) {
	if override == "" {
		return "", fmt.Errorf("--recipes requires a directory")
	}
	absOverride, err := filepath.Abs(override)
	if err != nil {
		return "", fmt.Errorf("resolving path: %w", err)
	}
	// Fail fast on a missing or non-directory path so a typo
	// (or pflag consuming a following flag as the value) yields
	// one clear error instead of misleading per-package
	// "no local recipe" misses (gh#114).
	info, err := os.Stat(absOverride)
	if err != nil {
		return "", fmt.Errorf("recipes directory not found: %s", absOverride)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("recipes path is not a directory: %s", absOverride)
	}
	// If override contains a recipes/ subdir, use that.
	sub := filepath.Join(absOverride, "recipes")
	if _, err := os.Stat(sub); err == nil {
		return sub, nil
	}
	return absOverride, nil
}

// detectRecipesRepo checks if the recipe file is inside a
// recipes repo (path contains /recipes/<letter>/<name>.toml).
// Returns the recipes root directory if detected, empty string
// otherwise.
func detectRecipesRepo(recipePath string) string {
	abs, err := filepath.Abs(recipePath)
	if err != nil {
		return ""
	}

	// Look for /recipes/<letter>/ in the path.
	normalized := filepath.ToSlash(abs)
	idx := strings.Index(normalized, "/recipes/")
	if idx < 0 {
		return ""
	}

	// Verify the structure: recipes/<single-char>/<name>.toml
	rest := normalized[idx+len("/recipes/"):]
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 2 {
		return ""
	}
	if len(parts[0]) != 1 {
		return ""
	}

	return filepath.FromSlash(normalized[:idx+len("/recipes")])
}

// recipeFileResolver returns a RecipeResolver that looks for
// recipes in the same repo as the given recipe file. Assumes
// letter-bucketed layout: recipes/<letter>/<name>.toml.
func recipeFileResolver(recipePath string) installer.RecipeResolver {
	absPath, err := filepath.Abs(recipePath)
	if err != nil {
		return func(_ string) (*recipe.Recipe, error) {
			return nil, fmt.Errorf("resolving recipe path: %w", err)
		}
	}
	// recipePath is like .../recipes/j/jq.toml
	// We want the directory containing "recipes/".
	dir := filepath.Dir(filepath.Dir(filepath.Dir(absPath)))
	return localRecipeResolver(filepath.Join(dir, "recipes"))
}
