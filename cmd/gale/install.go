package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/profile"
	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/store"
	"github.com/spf13/cobra"
)

var (
	installGlobal bool
	installRecipe string
)

var installCmd = &cobra.Command{
	Use:   "install <package>[@version]",
	Short: "Install a package",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, version := parsePackageArg(args[0])
		if version == "" {
			version = "latest"
		}

		out := output.New(os.Stderr, !cmd.Flags().Changed("no-color"))

		// If --recipe flag is provided, install from recipe file.
		if installRecipe != "" {
			return installFromRecipeFile(installRecipe, out)
		}

		// Otherwise, just add to gale.toml (full repo-based
		// install flow is not yet implemented).
		configPath, err := resolveConfigPath(installGlobal)
		if err != nil {
			return err
		}

		if err := config.AddPackage(configPath, name, version); err != nil {
			return fmt.Errorf("adding package: %w", err)
		}

		out.Success(fmt.Sprintf("Added %s@%s to %s",
			name, version, configPath))
		return nil
	},
}

func init() {
	installCmd.Flags().BoolVarP(&installGlobal, "global", "g",
		false, "Install to global config")
	installCmd.Flags().StringVar(&installRecipe, "recipe", "",
		"Install from a recipe TOML file")
	rootCmd.AddCommand(installCmd)
}

func installFromRecipeFile(recipePath string, out *output.Output) error {
	data, err := os.ReadFile(recipePath)
	if err != nil {
		return fmt.Errorf("reading recipe: %w", err)
	}

	r, err := recipe.Parse(string(data))
	if err != nil {
		return fmt.Errorf("parsing recipe: %w", err)
	}

	galeDir, err := galeConfigDir()
	if err != nil {
		return err
	}

	storeRoot := defaultStoreRoot()
	binDir := filepath.Join(galeDir, "bin")

	inst := &installer.Installer{
		Store:   store.NewStore(storeRoot),
		Profile: profile.NewProfile(binDir),
	}

	out.Info(fmt.Sprintf("Installing %s@%s...",
		r.Package.Name, r.Package.Version))

	result, err := inst.Install(r)
	if err != nil {
		return fmt.Errorf("install failed: %w", err)
	}

	switch result.Method {
	case "cached":
		out.Success(fmt.Sprintf("%s@%s already installed",
			result.Name, result.Version))
	case "binary":
		out.Success(fmt.Sprintf("Installed %s@%s from binary",
			result.Name, result.Version))
	case "source":
		out.Success(fmt.Sprintf("Installed %s@%s (built from source)",
			result.Name, result.Version))
	}

	return nil
}

// parsePackageArg splits "name@version" into name and version.
func parsePackageArg(arg string) (string, string) {
	if i := strings.LastIndex(arg, "@"); i > 0 {
		return arg[:i], arg[i+1:]
	}
	return arg, ""
}

// resolveConfigPath returns the gale.toml path to write to.
func resolveConfigPath(global bool) (string, error) {
	if global {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("finding home dir: %w", err)
		}
		return filepath.Join(home, ".gale", "gale.toml"), nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getting working dir: %w", err)
	}

	path, err := config.FindGaleConfig(cwd)
	if err == nil {
		return path, nil
	}

	return filepath.Join(cwd, "gale.toml"), nil
}
