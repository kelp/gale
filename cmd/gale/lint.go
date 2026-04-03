package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/kelp/gale/internal/lint"
	"github.com/kelp/gale/internal/output"
	"github.com/spf13/cobra"
)

var lintCmd = &cobra.Command{
	Use:   "lint <recipe.toml> [recipe.toml...]",
	Short: "Validate recipe files",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		out := output.New(os.Stderr, !cmd.Flags().Changed("no-color"))

		hasErrors := false
		for _, path := range args {
			if strings.HasSuffix(path, ".binaries.toml") ||
				strings.HasSuffix(path, ".versions") {
				continue
			}

			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("reading %s: %w", path, err)
			}

			issues := lint.Lint(string(data), path)
			if len(issues) == 0 {
				out.Success(fmt.Sprintf("%s: ok", path))
				continue
			}

			for _, issue := range issues {
				switch issue.Level {
				case "error":
					lintIssueOutput(out, lint.Issue{
						Level:   issue.Level,
						Message: fmt.Sprintf("%s: %s", path, issue.Message),
					})
					hasErrors = true
				case "warning":
					out.Info(fmt.Sprintf(
						"%s: %s", path, issue.Message))
				}
			}
		}

		if hasErrors {
			return fmt.Errorf("lint errors found")
		}
		return nil
	},
}

// lintIssueOutput outputs an error-level lint issue.
func lintIssueOutput(out *output.Output, issue lint.Issue) {
	out.Error(issue.Message)
}

func init() {
	rootCmd.AddCommand(lintCmd)
}
