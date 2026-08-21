package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/kelp/gale/internal/index"
	"github.com/kelp/gale/internal/output"
	"github.com/spf13/cobra"
)

var lintBase string

var lintCmd = &cobra.Command{
	Use:   "lint <file.toml> [file.toml...]",
	Short: "Validate index files",
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
	var first error
	for _, path := range args {
		if strings.HasSuffix(path, ".binaries.toml") ||
			strings.HasSuffix(path, ".versions") {
			continue
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}
		if !looksLikeIndex(data) {
			err := fmt.Errorf("%s: not an index document", path)
			out.Error(err.Error())
			if first == nil {
				first = err
			}
			failed = true
			continue
		}
		if err := lintOneIndex(out, path, data); err != nil {
			if first == nil {
				first = err
			}
			failed = true
		}
	}

	if failed {
		if first != nil {
			return first
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

func init() {
	lintCmd.Flags().StringVar(&lintBase, "base", "",
		"Previous index document for LintDiff")
	rootCmd.AddCommand(lintCmd)
}
