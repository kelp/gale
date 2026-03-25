package main

import (
	"fmt"
	"os"

	"github.com/kelp/gale/internal/output"
	"github.com/spf13/cobra"
)

var importCmd = &cobra.Command{
	Use:   "import homebrew [package]",
	Short: "Import packages from Homebrew",
	Long:  "Convert Homebrew packages to gale.toml entries. Requires an API key in ~/.gale/config.toml.",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		source := args[0]
		if source != "homebrew" {
			return fmt.Errorf("unsupported import source: %s (only 'homebrew' is supported)", source)
		}

		out := output.New(os.Stderr, !cmd.Flags().Changed("no-color"))

		galeDir, err := galeConfigDir()
		if err != nil {
			return err
		}

		client := loadAIClient(galeDir)
		if client == nil {
			return fmt.Errorf("import requires an AI API key in ~/.gale/config.toml")
		}

		pkg := ""
		if len(args) > 1 {
			pkg = args[1]
		}

		prompt := "Read the output of 'brew list' and generate gale.toml [packages] entries for each installed formula. Output only the TOML."
		if pkg != "" {
			prompt = fmt.Sprintf(
				"Translate the Homebrew formula for %q into a gale.toml package entry. Output only the TOML line like: %s = \"version\".",
				pkg, pkg)
		}

		out.Info("Translating Homebrew packages...")
		result, err := client.Complete(prompt)
		if err != nil {
			return fmt.Errorf("AI translation: %w", err)
		}

		fmt.Println(result)
		out.Success("Import complete")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(importCmd)
}
