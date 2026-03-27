package main

import (
	"fmt"

	"github.com/kelp/gale/internal/env"
	"github.com/spf13/cobra"
)

var hookCmd = &cobra.Command{
	Use:   "hook <shell>",
	Short: "Print shell integration script",
	Long: `Print a script that integrates gale with your shell.

  eval "$(gale hook direnv)"  # ~/.config/direnv/direnvrc
  eval "$(gale hook zsh)"     # .zshrc (legacy)
  eval "$(gale hook bash)"    # .bashrc (legacy)
  gale hook fish | source     # config.fish (legacy)`,
	Args:      cobra.ExactArgs(1),
	ValidArgs: []string{"direnv", "fish", "zsh", "bash"},
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
