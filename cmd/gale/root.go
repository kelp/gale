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
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		applyNoColor()
	},
}

func init() {
	rootCmd.PersistentFlags().BoolVar(&noColor, "no-color",
		false, "Disable colored output")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose",
		"v", false, "Verbose output")
	rootCmd.PersistentFlags().BoolVarP(&dryRun, "dry-run",
		"n", false, "Show what would happen without doing it")
	rootCmd.SetHelpFunc(colorHelp)
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
