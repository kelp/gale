package main

import (
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "gale",
	Short: "Fast, isolated package management for developers",
	Long: `Gale is a package manager for CLI tools and runtimes. It installs
into isolated, versioned directories. Per-project environments
activate automatically and stay out of your way.`,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
