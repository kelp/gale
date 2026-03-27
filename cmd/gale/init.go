package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kelp/gale/internal/output"
	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize a project for gale",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		out := output.New(os.Stderr,
			!cmd.Flags().Changed("no-color"))

		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working dir: %w", err)
		}

		// Create gale.toml if it doesn't exist.
		galePath := filepath.Join(cwd, "gale.toml")
		if err := writeIfNotExists(galePath,
			"[packages]\n"); err != nil {
			return fmt.Errorf("creating gale.toml: %w", err)
		}
		out.Success("Created gale.toml")

		// Create .envrc if it doesn't exist.
		envrcPath := filepath.Join(cwd, ".envrc")
		if err := writeIfNotExists(envrcPath,
			"use gale\n"); err != nil {
			return fmt.Errorf("creating .envrc: %w", err)
		}
		out.Success("Created .envrc")

		// Append .gale/ to .gitignore if not present.
		if err := appendToGitignore(cwd, ".gale/"); err != nil {
			return fmt.Errorf("updating .gitignore: %w", err)
		}
		out.Success("Added .gale/ to .gitignore")

		out.Info("Run 'direnv allow' to activate.")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(initCmd)
}

// writeIfNotExists creates a file with content only if
// it doesn't already exist.
func writeIfNotExists(path, content string) error {
	if _, err := os.Stat(path); err == nil {
		return nil // already exists
	}
	return os.WriteFile(path, []byte(content), 0o644) //nolint:gosec // G306 — project files should be world-readable
}

// appendToGitignore adds a line to .gitignore if not
// already present. Creates the file if needed.
func appendToGitignore(dir, line string) error { //nolint:unparam // line is a param for testability
	path := filepath.Join(dir, ".gitignore")

	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	// Check if the line is already present.
	for _, existing := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(existing) == line {
			return nil
		}
	}

	// Append the line.
	f, err := os.OpenFile(path,
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	// Add newline before if file doesn't end with one.
	if len(data) > 0 && data[len(data)-1] != '\n' {
		if _, err := f.WriteString("\n"); err != nil {
			return err
		}
	}

	_, err = f.WriteString(line + "\n")
	return err
}
