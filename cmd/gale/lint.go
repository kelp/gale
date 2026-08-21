package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/kelp/gale/internal/index"
	"github.com/kelp/gale/internal/lint"
	"github.com/kelp/gale/internal/output"
	"github.com/spf13/cobra"
)

var (
	lintStrict bool
	lintBase   string
)

var lintCmd = &cobra.Command{
	Use:   "lint <file.toml> [file.toml...]",
	Short: "Validate recipe or index files",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runLint,
}

func runLint(cmd *cobra.Command, args []string) error {
	out := newCmdOutput(cmd)
	if lintBase != "" {
		if len(args) != 1 {
			return fmt.Errorf("lint --base requires exactly one file")
		}
		return lintIndexWithBase(out, lintBase, args[0])
	}

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
		if looksLikeIndex(data) {
			if err := lintOneIndex(out, path, data); err != nil {
				failed = true
			}
			continue
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
}

func looksLikeIndex(data []byte) bool {
	ok, err := probeIndex(data)
	return err == nil && ok
}

func probeIndex(data []byte) (bool, error) {
	var raw map[string]any
	if _, err := toml.Decode(string(data), &raw); err != nil {
		return false, err
	}
	_, ok := raw["versions"]
	return ok, nil
}

func requireIndexDoc(label string, data []byte) error {
	ok, err := probeIndex(data)
	if err != nil {
		return fmt.Errorf("parsing %s: %w", label, err)
	}
	if !ok {
		return fmt.Errorf("lint --base requires index documents")
	}
	return nil
}

func lintIndexWithBase(out *output.Output, base, path string) error {
	oldData, err := os.ReadFile(base)
	if err != nil {
		return fmt.Errorf("reading %s: %w", base, err)
	}
	newData, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	if err := requireIndexDoc(base, oldData); err != nil {
		return err
	}
	if err := requireIndexDoc(path, newData); err != nil {
		return err
	}
	oldFile, err := index.Parse(oldData)
	if err != nil {
		return fmt.Errorf("parsing %s: %w", base, err)
	}
	newFile, err := parseIndexForLint(out, path, newData)
	if err != nil {
		return err
	}
	issues := indexIssues(path, newFile)
	issues = append(issues, index.LintDiff(oldFile, newFile)...)
	return emitIndexResult(out, path, issues)
}

func lintOneIndex(out *output.Output, path string, data []byte) error {
	f, err := parseIndexForLint(out, path, data)
	if err != nil {
		return err
	}
	return emitIndexResult(out, path, indexIssues(path, f))
}

func parseIndexForLint(out *output.Output, path string, data []byte) (*index.File, error) {
	f, err := index.Parse(data)
	if err != nil {
		out.Error(fmt.Sprintf("%s: %s", path, err))
		return nil, errors.New("lint errors found")
	}
	return f, nil
}

func indexIssues(path string, f *index.File) []index.Issue {
	issues := index.Lint(f)
	stem := strings.TrimSuffix(filepath.Base(path), ".toml")
	if f.Package.Name != stem {
		issues = append(issues, index.Issue{
			Path:    "package.name",
			Message: "package name does not match filename",
		})
	}
	return issues
}

func emitIndexResult(out *output.Output, path string, issues []index.Issue) error {
	if len(issues) == 0 {
		out.Success(fmt.Sprintf("%s: ok", path))
		return nil
	}
	emitIndexIssues(out, path, issues)
	return errors.New("lint errors found")
}

func emitIndexIssues(out *output.Output, path string, issues []index.Issue) {
	for _, issue := range issues {
		if issue.Path == "" {
			out.Error(fmt.Sprintf("%s: %s", path, issue.Message))
			continue
		}
		out.Error(fmt.Sprintf("%s: %s: %s", path, issue.Path, issue.Message))
	}
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
	f := lintCmd.Flags()
	f.BoolVar(&lintStrict, "strict", false,
		"Fail on warnings as well as errors")
	f.StringVar(&lintBase, "base", "",
		"Previous index document for LintDiff")
	rootCmd.AddCommand(lintCmd)
}
