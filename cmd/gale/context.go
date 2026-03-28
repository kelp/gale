package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/store"
)

// cmdContext holds resolved config, store, and installer
// shared by sync, update, and install commands.
type cmdContext struct {
	GalePath  string // path to gale.toml
	GaleDir   string // .gale directory (project or global)
	StoreRoot string
	Resolver  installer.RecipeResolver
	Installer *installer.Installer
}

// newCmdContext resolves the config, store, and installer.
// If local is true, recipes are resolved from a sibling
// gale-recipes directory.
func newCmdContext(local bool) (*cmdContext, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("getting working dir: %w", err)
	}

	// Find config: project gale.toml first, then global.
	galePath, err := config.FindGaleConfig(cwd)
	if err != nil {
		globalDir, dirErr := galeConfigDir()
		if dirErr != nil {
			return nil, dirErr
		}
		galePath = filepath.Join(globalDir, "gale.toml")
	}

	// Resolve galeDir: project .gale/ or global ~/.gale/.
	galeDir, err := galeConfigDir()
	if err != nil {
		return nil, err
	}
	configDir := filepath.Dir(galePath)
	globalDir, _ := galeConfigDir()
	if configDir != globalDir {
		galeDir = filepath.Join(configDir, ".gale")
	}

	// Set up resolver.
	storeRoot := defaultStoreRoot()
	var resolver installer.RecipeResolver
	if local {
		recipesDir, dirErr := findLocalRecipesDir(cwd)
		if dirErr != nil {
			return nil, dirErr
		}
		resolver = localRecipeResolver(recipesDir)
	} else {
		reg := newRegistry()
		resolver = reg.FetchRecipe
	}

	inst := &installer.Installer{
		Store:    store.NewStore(storeRoot),
		Resolver: resolver,
	}

	return &cmdContext{
		GalePath:  galePath,
		GaleDir:   galeDir,
		StoreRoot: storeRoot,
		Resolver:  resolver,
		Installer: inst,
	}, nil
}

// installPackage resolves a recipe by name and installs it.
// Returns the install result. Does not update config or
// rebuild the generation — callers handle that.
func (ctx *cmdContext) installPackage(name string, out *output.Output) (*installer.InstallResult, error) {
	r, err := ctx.Resolver(name)
	if err != nil {
		return nil, err
	}

	out.Info(fmt.Sprintf("Installing %s@%s...",
		r.Package.Name, r.Package.Version))

	result, err := ctx.Installer.Install(r)
	if err != nil {
		return nil, fmt.Errorf("install %s: %w", name, err)
	}

	return result, nil
}
