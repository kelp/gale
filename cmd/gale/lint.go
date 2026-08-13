package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/kelp/gale/internal/lint"
	"github.com/kelp/gale/internal/output"
	"github.com/spf13/cobra"
)

var lintStrict bool

var lintCmd = &cobra.Command{
	Use:   "lint <recipe.toml> [recipe.toml...]",
	Short: "Validate recipe files",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		out := newCmdOutput(cmd)

		failed := false
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

			// --strict fails on any issue. Warnings are the
			// only reason a recipe CI step over a whole tree
			// stays green while a rule fires (gale-recipes#189).
			if emitLintIssues(out, path, issues) || lintStrict {
				failed = true
			}
		}

		if failed {
			if lintStrict {
				return errors.New("lint issues found")
			}
			return errors.New("lint errors found")
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
			out.Error(msg)
			hasErrors = true
		case "warning":
			out.Warn(msg)
		}
	}
	return hasErrors
}

func init() {
	lintCmd.Flags().BoolVar(&lintStrict, "strict", false,
		"Fail on warnings as well as errors")
	rootCmd.AddCommand(lintCmd)
}
