package main

import (
	"fmt"

	"github.com/kelp/gale/internal/env"
	"github.com/spf13/cobra"
)

var hookCmd = &cobra.Command{
	Use:   "hook <shell>",
	Short: "Print shell integration script",
	Long: `Print a script that integrates gale with direnv.

Add to ~/.config/direnv/direnvrc:

  eval "$(gale hook direnv)"`,
	Args:      cobra.ExactArgs(1),
	ValidArgs: []string{"direnv"},
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
