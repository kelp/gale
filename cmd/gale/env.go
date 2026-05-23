package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kelp/gale/internal/config"
	"github.com/spf13/cobra"
)

var envVarsOnly bool

var envCmd = &cobra.Command{
	Use:   "env",
	Short: "Print shell commands to activate the environment",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		out := cmd.OutOrStdout()

		if !envVarsOnly {
			galeDir, err := resolveGaleDir()
			if err != nil {
				return err
			}
			binDir := filepath.Join(galeDir, "current", "bin")
			fmt.Fprintf(out, "export PATH=\"%s:$PATH\"\n", binDir)
		}

		// Export [vars] from gale.toml. Prefer the project
		// config; if none is in the walk-up chain, fall back
		// to the global ~/.gale/gale.toml so global [vars]
		// are honoured when env resolves to the global scope.
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working dir: %w", err)
		}
		configPath, err := config.FindGaleConfig(cwd)
		if err != nil {
			if !errors.Is(err, config.ErrGaleConfigNotFound) {
				return fmt.Errorf(
					"locating gale.toml: %w", err)
			}
			// No project config — try the global config.
			globalDir, gerr := galeConfigDir()
			if gerr != nil {
				return gerr
			}
			globalConfig := filepath.Join(
				globalDir, "gale.toml")
			if _, sterr := os.Stat(globalConfig); sterr != nil {
				if errors.Is(sterr, os.ErrNotExist) {
					// No config anywhere — nothing to export.
					return nil
				}
				return fmt.Errorf(
					"stat global gale.toml: %w", sterr)
			}
			configPath = globalConfig
		}
		data, err := os.ReadFile(configPath)
		if err != nil {
			return fmt.Errorf(
				"reading %s: %w", configPath, err)
		}
		cfg, err := config.ParseGaleConfig(string(data))
		if err != nil {
			return fmt.Errorf(
				"parsing %s: %w", configPath, err)
		}

		// Sort keys for deterministic output.
		keys := make([]string, 0, len(cfg.Vars))
		for k := range cfg.Vars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(out, "export %s='%s'\n",
				k, shellEscape(cfg.Vars[k]))
		}

		return nil
	},
}

func init() {
	envCmd.Flags().BoolVar(&envVarsOnly, "vars-only", false,
		"Only print variable exports, not PATH")
	rootCmd.AddCommand(envCmd)
}

// resolveGaleDir returns the .gale directory for the
// current scope. If a project gale.toml exists, returns
// the project's .gale/ dir. Otherwise returns ~/.gale/.
func resolveGaleDir() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getting working dir: %w", err)
	}

	// Check for project gale.toml.
	projectConfig, err := config.FindGaleConfig(cwd)
	if err == nil {
		return filepath.Join(
			filepath.Dir(projectConfig), ".gale"), nil
	}

	// Fall back to global.
	return galeConfigDir()
}

// shellEscape escapes a string for use inside single
// quotes in a POSIX shell. Embedded single quotes are
// replaced with the '\” idiom (end quote, escaped
// literal quote, re-open quote).
func shellEscape(s string) string {
	return strings.ReplaceAll(s, "'", "'\\''")
}
