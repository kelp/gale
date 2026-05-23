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
		out := newCmdOutput(cmd)

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

			if emitLintIssues(out, path, issues) {
				hasErrors = true
			}
		}

		if hasErrors {
			return fmt.Errorf("lint errors found")
		}
		return nil
	},
}

// emitLintIssues writes each lint issue to out, mapping
// severity → prefix consistently with the rest of gale:
// errors use out.Error (red `xxx `), warnings use out.Warn
// (yellow `!!! `). Returns true if at least one error-level
// issue was emitted, so the caller can set a failing exit
// status.
func emitLintIssues(
	out *output.Output, path string, issues []lint.Issue,
) bool {
	hasErrors := false
	for _, issue := range issues {
		msg := fmt.Sprintf("%s: %s", path, issue.Message)
		switch issue.Level {
		case "error":
			lintIssueOutput(out, lint.Issue{
				Level:   issue.Level,
				Message: msg,
			})
			hasErrors = true
		case "warning":
			out.Warn(msg)
		}
	}
	return hasErrors
}

// lintIssueOutput outputs an error-level lint issue.
func lintIssueOutput(out *output.Output, issue lint.Issue) {
	out.Error(issue.Message)
}

func init() {
	rootCmd.AddCommand(lintCmd)
}
