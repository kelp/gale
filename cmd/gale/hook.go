package main

import (
	"fmt"

	"github.com/kelp/gale/internal/env"
	"github.com/spf13/cobra"
)

var hookCmd = &cobra.Command{
	Use:       "hook <shell>",
	Short:     "Output shell hook for environment activation",
	Long:      "Prints a shell script to eval in your shell config for auto-activation.",
	Args:      cobra.ExactArgs(1),
	ValidArgs: []string{"fish", "zsh", "bash"},
	RunE: func(cmd *cobra.Command, args []string) error {
		script, err := env.GenerateHook(args[0])
		if err != nil {
			return err
		}
		fmt.Print(script)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(hookCmd)
}
