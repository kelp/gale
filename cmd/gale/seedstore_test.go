package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kelp/gale/internal/depsmeta"
	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/store"
)

func writeDepsMeta(
	t *testing.T, storeRoot, name, version string, deps ...depsmeta.ResolvedDep,
) {
	t.Helper()
	if err := depsmeta.Write(
		filepath.Join(storeRoot, name, version),
		depsmeta.Metadata{Deps: deps},
	); err != nil {
		t.Fatal(err)
	}
}

func runtimeDepRecipe(name, version string, deps ...string) *recipe.Recipe {
	return &recipe.Recipe{
		Package:      recipe.Package{Name: name, Version: version},
		Dependencies: recipe.Dependencies{Runtime: deps},
	}
}

func buildFakeCtx(
	t *testing.T,
	galePath, galeDir, storeRoot string,
	resolver installer.RecipeResolver,
) *cmdContext {
	t.Helper()
	inst := &installer.Installer{
		Store:    store.NewStore(storeRoot),
		Resolver: resolver,
		Verifier: nil,
	}
	return &cmdContext{
		GalePath:  galePath,
		GaleDir:   galeDir,
		StoreRoot: storeRoot,
		Resolver:  resolver,
		Installer: inst,
		Registry:  nil,
	}
}

func minimalRecipe(name, version string) *recipe.Recipe {
	return &recipe.Recipe{
		Package: recipe.Package{
			Name:    name,
			Version: version,
		},
	}
}

// seedStore creates a canonical store dir for name/version
// and writes a bin/<name> placeholder so IsInstalled returns true.
func seedStore(t *testing.T, storeRoot, name, version string) string {
	t.Helper()
	s := store.NewStore(storeRoot)
	dir, err := s.Create(name, version)
	if err != nil {
		t.Fatalf("seedStore Create: %v", err)
	}
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("seedStore MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(binDir, name),
		[]byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("seedStore WriteFile: %v", err)
	}
	return dir
}
