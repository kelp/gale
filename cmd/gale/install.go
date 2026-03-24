package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/output"
	"github.com/spf13/cobra"
)

var installGlobal bool

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
	rootCmd.AddCommand(installCmd)
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

	// Look for project gale.toml, fall back to global.
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getting working dir: %w", err)
	}

	path, err := config.FindGaleConfig(cwd)
	if err == nil {
		return path, nil
	}

	// No project config found — use local gale.toml.
	return filepath.Join(cwd, "gale.toml"), nil
}
