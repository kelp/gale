package main

import (
	"os"

	"github.com/spf13/cobra"
)

// version is set at build time via ldflags:
//
//	go build -ldflags "-X main.version=abc1234"
var version = "dev"

var rootCmd = &cobra.Command{
	Use:     "gale",
	Short:   "Fast, isolated package management for developers",
	Version: version,
	Long: `Gale is a package manager for developer tools and runtimes.
Each version installs in its own directory — nothing conflicts.
Projects get isolated environments, activated automatically on cd.`,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
