package main

import (
	"fmt"
	"os"

	"github.com/kelp/gale/internal/homebrew"
	"github.com/kelp/gale/internal/output"
	"github.com/spf13/cobra"
)

var importCmd = &cobra.Command{
	Use:   "import homebrew <package>",
	Short: "Generate a recipe from a Homebrew formula",
	Long: `Fetch a Homebrew formula from GitHub and generate a gale
recipe TOML file. Does not require Homebrew to be installed.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		source := args[0]
		if source != "homebrew" {
			return fmt.Errorf("unsupported source: %s (only 'homebrew' is supported)", source)
		}

		pkg := args[1]
		out := output.New(os.Stderr, !cmd.Flags().Changed("no-color"))

		out.Info(fmt.Sprintf("Fetching formula for %s...", pkg))

		f, err := homebrew.FetchFormula(pkg, "https://raw.githubusercontent.com/Homebrew/homebrew-core/master/Formula")
		if err != nil {
			return fmt.Errorf("fetching formula: %w", err)
		}

		fmt.Print(f.ToRecipeTOML())

		out.Success(fmt.Sprintf("Recipe generated for %s@%s",
			f.Name, f.Version))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(importCmd)
}
