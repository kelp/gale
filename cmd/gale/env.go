package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/kelp/gale/internal/config"
	"github.com/spf13/cobra"
)

var envCmd = &cobra.Command{
	Use:   "env",
	Short: "Print shell commands to activate the environment",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		galeDir, err := resolveGaleDir()
		if err != nil {
			return err
		}

		binDir := filepath.Join(galeDir, "current", "bin")
		fmt.Fprintf(cmd.OutOrStdout(),
			"export PATH=\"%s:$PATH\"\n", binDir)
		return nil
	},
}

func init() {
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
