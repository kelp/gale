package main

import (
	"os"

	"github.com/spf13/cobra"
)

var completionCmd = &cobra.Command{
	Use:   "completion <shell>",
	Short: "Generate shell completion script",
	Long: `Generate a completion script for bash, zsh, fish, or powershell.

Bash:
  gale completion bash > /usr/local/share/bash-completion/completions/gale

Zsh:
  gale completion zsh > "${fpath[1]}/_gale"

Fish:
  gale completion fish > ~/.config/fish/completions/gale.fish

PowerShell:
  gale completion powershell > gale.ps1`,
}

var completionBashCmd = &cobra.Command{
	Use:   "bash",
	Short: "Generate bash completion script",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return rootCmd.GenBashCompletionV2(os.Stdout, true)
	},
}

var completionZshCmd = &cobra.Command{
	Use:   "zsh",
	Short: "Generate zsh completion script",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return rootCmd.GenZshCompletion(os.Stdout)
	},
}

var completionFishCmd = &cobra.Command{
	Use:   "fish",
	Short: "Generate fish completion script",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return rootCmd.GenFishCompletion(os.Stdout, true)
	},
}

var completionPowershellCmd = &cobra.Command{
	Use:   "powershell",
	Short: "Generate powershell completion script",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return rootCmd.GenPowerShellCompletionWithDesc(os.Stdout)
	},
}

func init() {
	completionCmd.AddCommand(
		completionBashCmd,
		completionZshCmd,
		completionFishCmd,
		completionPowershellCmd,
	)
	rootCmd.AddCommand(completionCmd)
}
