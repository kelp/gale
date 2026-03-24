package main

import (
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "gale",
	Short: "A macOS-first package manager for developer tools",
	Long: `Gale is a package manager that combines Homebrew's simplicity
with Nix's isolation. Install CLI tools and language runtimes
from prebuilt binaries with per-project environments.`,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
