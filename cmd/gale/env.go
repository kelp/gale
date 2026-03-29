package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

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

		// Export [vars] from gale.toml.
		cwd, err := os.Getwd()
		if err != nil {
			return nil //nolint:nilerr // best-effort
		}
		configPath, err := config.FindGaleConfig(cwd)
		if err != nil {
			return nil //nolint:nilerr // no project config
		}
		data, err := os.ReadFile(configPath)
		if err != nil {
			return nil //nolint:nilerr // best-effort
		}
		cfg, err := config.ParseGaleConfig(string(data))
		if err != nil {
			return nil //nolint:nilerr // best-effort
		}

		// Sort keys for deterministic output.
		keys := make([]string, 0, len(cfg.Vars))
		for k := range cfg.Vars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(out, "export %s=%q\n", k, cfg.Vars[k])
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
