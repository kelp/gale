package main

import (
	"fmt"

	"github.com/kelp/gale/internal/env"
	"github.com/spf13/cobra"
)

var hookCmd = &cobra.Command{
	Use:   "hook <shell>",
	Short: "Print shell integration script",
	Long: `Print a script that activates project environments on cd.
Add to your shell config:

  eval "$(gale hook zsh)"     # .zshrc
  eval "$(gale hook bash)"    # .bashrc
  gale hook fish | source     # config.fish`,
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
