package main

import (
	"encoding/json"
	"os"

	"github.com/spf13/cobra"
)

// version is set at build time via ldflags:
//
//	go build -ldflags "-X main.version=abc1234"
var version = "dev"

var commandStarted bool

var rootCmd = &cobra.Command{
	Use:     "gale",
	Short:   "Fast, isolated package management for developers",
	Version: version,
	Long: `Gale is a package manager for developer tools and runtimes.
Each version installs in its own directory — nothing conflicts.
Projects get isolated environments, activated automatically on cd.`,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		commandStarted = true
		cmd.SilenceUsage = true
		cmd.SilenceErrors = currentOutputMode().errorFormat == "json"
		applyColorMode()
	},
}

func init() {
	rootCmd.PersistentFlags().BoolVar(&noColor, "no-color",
		false, "Disable colored output")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose",
		"v", false, "Verbose output")
	rootCmd.PersistentFlags().BoolVarP(&dryRun, "dry-run",
		"n", false, "Show what would happen without doing it")
	rootCmd.PersistentFlags().BoolVar(&plain, "plain",
		false, "Disable color and progress output")
	rootCmd.PersistentFlags().BoolVarP(&quiet, "quiet",
		"q", false, "Suppress non-essential status output")
	rootCmd.PersistentFlags().StringVar(&errorFormat, "error-format",
		"text", "Format for top-level errors: text or json")
	rootCmd.SetHelpFunc(colorHelp)
}

func executeRoot() error {
	commandStarted = false
	cmd, err := rootCmd.ExecuteC()
	if err != nil && commandStarted && currentOutputMode().errorFormat == "json" {
		payload := struct {
			Kind    string `json:"kind"`
			Command string `json:"command"`
			Message string `json:"message"`
		}{
			Kind:    "runtime",
			Message: err.Error(),
		}
		if cmd != nil {
			payload.Command = cmd.Name()
		}
		_ = json.NewEncoder(rootCmd.ErrOrStderr()).Encode(payload)
	}
	return err
}

func Execute() {
	if err := executeRoot(); err != nil {
		os.Exit(1)
	}
}
