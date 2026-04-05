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
// the --recipes flag value and working directory. When
// recipesFlag is non-empty, returns a local resolver.
// Otherwise returns a registry-based resolver. The returned
// registry is nil when using local recipes.
func resolveRecipeResolver(recipesFlag, cwd string) (installer.RecipeResolver, *registry.Registry, error) {
	if recipesFlag != "" {
		override := ""
		if recipesFlag != "auto" {
			override = recipesFlag
		}
		recipesDir, err := findLocalRecipesDir(cwd, override)
		if err != nil {
			return nil, nil, err
		}
		return localRecipeResolver(recipesDir), nil, nil
	}
	reg := newRegistry()
	return reg.FetchRecipe, reg, nil
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
				return nil, fmt.Errorf(
					"no local recipe for %q", name)
			}
			return nil, fmt.Errorf("reading recipe %q: %w", name, err)
		}
		rec, err := recipe.Parse(string(data))
		if err != nil {
			return nil, err
		}

		// If recipe has no inline binaries, try the
		// separate .binaries.toml file.
		if len(rec.Binary) == 0 {
			binPath := filepath.Join(
				recipesDir, letter, name+".binaries.toml")
			binData, readErr := os.ReadFile(binPath)
			if readErr == nil {
				idx, parseErr := recipe.ParseBinaryIndex(
					string(binData))
				if parseErr == nil {
					recipe.MergeBinaries(
						rec, idx, localGHCRBase)
				}
			}
		}

		return rec, nil
	}
}

// findLocalRecipesDir locates a local recipes directory.
// When override is non-empty, it resolves that path directly
// (using its recipes/ subdirectory if present). When override
// is empty, it looks for a sibling gale-recipes directory
// relative to dir.
func findLocalRecipesDir(dir, override string) (string, error) {
	if override != "" {
		absOverride, err := filepath.Abs(override)
		if err != nil {
			return "", fmt.Errorf("resolving path: %w", err)
		}
		// If override contains a recipes/ subdir, use that.
		sub := filepath.Join(absOverride, "recipes")
		if _, err := os.Stat(sub); err == nil {
			return sub, nil
		}
		return absOverride, nil
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolving path: %w", err)
	}
	recipesDir := filepath.Join(filepath.Dir(absDir), "gale-recipes", "recipes")
	if _, err := os.Stat(recipesDir); err != nil {
		return "", fmt.Errorf(
			"no sibling gale-recipes found next to %s", absDir)
	}
	return recipesDir, nil
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
