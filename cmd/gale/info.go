package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/registry"
	"github.com/kelp/gale/internal/store"
	"github.com/spf13/cobra"
)

// registryOverride lets tests inject a custom registry. When
// nil, newRegistry() is used.
var registryOverride func() *registry.Registry

func infoRegistry() *registry.Registry {
	if registryOverride != nil {
		return registryOverride()
	}
	return newRegistry()
}

var (
	infoGlobal  bool
	infoProject bool
)

var infoCmd = &cobra.Command{
	Use:   "info <package>[@version]",
	Short: "Show package information",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runInfo(cmd.OutOrStdout(), args[0])
	},
}

// runInfo prints package metadata for arg (which may be
// "<name>" or "<name>@<version>") to w. The version form
// resolves through FetchRecipeVersion; the bare form checks
// project/global config first, then falls back to a single
// metadata fetch from the registry. Validation of <name>
// happens inside the registry layer (registry.ValidName).
//
// Scope flags (--global / --project) narrow the installed
// lookup to a single config — `info -g jq` from inside a
// project reads ~/.gale/gale.toml directly, bypassing the
// project-shadowing default.
func runInfo(w io.Writer, arg string) error {
	if err := validateScopeFlags(infoGlobal, infoProject); err != nil {
		return err
	}

	name, version := parsePackageArg(arg)

	// "name@" / "name@@" — parsePackageArg returns version=""
	// for both, which would silently fall through to the
	// "latest" branch. Reject explicitly so the user sees the
	// real problem.
	if strings.Contains(arg, "@") && version == "" {
		return fmt.Errorf(
			"invalid argument %q: expected <name>[@version]", arg)
	}

	// Pin the validation contract here too — `info` is the
	// canonical reproducer for audit/readonly/bad-input/0002
	// and pre-validating gives a clean error before any config
	// lookups touch the filesystem.
	if err := registry.ValidName(name); err != nil {
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working dir: %w", err)
	}

	// Scope-flag fast path: consult only the requested config.
	// Versioned form always defers to the registry, so flags
	// only affect the unversioned lookup.
	if version == "" && (infoGlobal || infoProject) {
		configPath, err := resolveReadOnlyConfigPath(
			infoGlobal, infoProject)
		if err != nil {
			return err
		}
		scope := "project"
		if infoGlobal {
			scope = "global"
		}
		found, err := printConfigInfo(w, name, configPath, scope)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("%s not found in %s gale.toml",
				name, scope)
		}
		return nil
	}

	// Versioned form bypasses the installed-config lookup —
	// users asking for a specific @version want registry
	// metadata, not whatever happens to be pinned locally.
	if version == "" {
		// Check project config first.
		if projectPath, pErr := config.FindGaleConfig(cwd); pErr == nil {
			if found, err := printConfigInfo(
				w, name, projectPath, "project"); err != nil {
				return err
			} else if found {
				return nil
			}
		}

		// Check global config.
		globalDir, err := galeConfigDir()
		if err != nil {
			return err
		}
		globalPath := filepath.Join(globalDir, "gale.toml")
		if found, err := printConfigInfo(
			w, name, globalPath, "global"); err != nil {
			return err
		} else if found {
			return nil
		}
	}

	// Not installed (or versioned form requested) — fetch from
	// registry. FetchRecipeMetadata skips the .binaries.toml
	// roundtrip the legacy code paid on every invocation; see
	// audit/readonly/network-perf/0005.
	reg := infoRegistry()
	var r *recipe.Recipe
	if version != "" {
		r, err = reg.FetchRecipeVersion(name, version)
		if err != nil {
			return fmt.Errorf("%s@%s: %w", name, version, err)
		}
	} else {
		r, err = reg.FetchRecipeMetadata(name)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}

	versionLabel := r.Package.Version
	if version == "" {
		versionLabel += " (latest)"
	}

	fmt.Fprintf(w, "Name:    %s\n", r.Package.Name)
	fmt.Fprintf(w, "Version: %s\n", versionLabel)
	if r.Package.Description != "" {
		fmt.Fprintf(w, "About:   %s\n", r.Package.Description)
	}
	if r.Source.URL != "" {
		fmt.Fprintf(w, "Source:  %s\n", r.Source.URL)
	}
	fmt.Fprintln(w, "(not installed)")

	return nil
}

// printConfigInfo checks if name is in the config at
// configPath and prints its info to w. Returns true if the
// package was found.
func printConfigInfo(w io.Writer, name, configPath, scope string) (bool, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("reading %s: %w",
			configPath, err)
	}

	cfg, err := config.ParseGaleConfig(string(data))
	if err != nil {
		return false, fmt.Errorf("parsing %s: %w",
			configPath, err)
	}
	cfg.ApplyHost(config.CurrentHost())

	version, ok := cfg.Packages[name]
	if !ok {
		return false, nil
	}

	storeRoot := defaultStoreRoot()
	s := store.NewStore(storeRoot)

	fmt.Fprintf(w, "Name:    %s\n", name)
	fmt.Fprintf(w, "Version: %s\n", version)
	if s.IsInstalled(name, version) {
		fmt.Fprintf(w, "Store:   %s\n",
			filepath.Join(storeRoot, name, version))
	}
	fmt.Fprintf(w, "Scope:   %s\n", scope)
	fmt.Fprintf(w, "Config:  %s\n", configPath)

	pinned := "no"
	if cfg.Pinned[name] {
		pinned = "yes"
	}
	fmt.Fprintf(w, "Pinned:  %s\n", pinned)

	return true, nil
}

func init() {
	infoCmd.Flags().BoolVarP(&infoGlobal, "global", "g", false,
		"Look up the package in the global gale.toml")
	infoCmd.Flags().BoolVarP(&infoProject, "project", "p", false,
		"Look up the package in the project gale.toml")
	rootCmd.AddCommand(infoCmd)
}
