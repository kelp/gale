package main

import (
	"fmt"

	"github.com/kelp/gale/internal/env"
	"github.com/spf13/cobra"
)

var hookCmd = &cobra.Command{
	Use:   "hook direnv",
	Short: "Print shell integration script",
	Long: `Print a script that integrates gale with direnv.

Currently only direnv is supported. Add to
~/.config/direnv/direnvrc:

  eval "$(gale hook direnv)"`,
	// ExactArgs(1) by itself ignores ValidArgs. Compose with
	// OnlyValidArgs so an unknown shell is rejected at the
	// cobra layer with a diagnostic listing the accepted
	// values, rather than falling through to a generic
	// "unsupported shell" error from env.GenerateHook.
	Args: cobra.MatchAll(
		cobra.ExactArgs(1), cobra.OnlyValidArgs,
	),
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
