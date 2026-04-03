package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/lint"
	"github.com/kelp/gale/internal/output"
)

// BUG-7: lint.go reports error-level issues using out.Warn
// instead of out.Error.

func TestLintErrorLevelUsesErrorOutput(t *testing.T) {
	// Create a recipe with a lint error (missing required
	// field: package.name).
	data := `
[package]
version = "1.0"
[source]
url = "https://example.com/foo.tar.gz"
sha256 = "2be64e7129cecb11d5906290eba10af694fb9e3e7f9fc208a311dc33ca837eb0"
[build]
steps = ["make install PREFIX=${PREFIX}"]
`
	dir := t.TempDir()
	path := filepath.Join(dir, "test.toml")
	if err := os.WriteFile(
		path, []byte(data), 0o644); err != nil {
		t.Fatalf("writing test recipe: %v", err)
	}

	issues := lint.Lint(data, path)
	if len(issues) == 0 {
		t.Fatal("expected lint issues")
	}

	// Verify at least one error-level issue exists.
	hasErr := false
	for _, issue := range issues {
		if issue.Level == "error" {
			hasErr = true
			break
		}
	}
	if !hasErr {
		t.Fatal("expected at least one error-level issue")
	}

	// Simulate the output dispatch from lintCmd and verify
	// error-level issues use out.Error (prefix "xxx "), not
	// out.Warn (prefix "!!! ").
	var buf bytes.Buffer
	out := output.New(&buf, false)

	for _, issue := range issues {
		switch issue.Level {
		case "error":
			lintIssueOutput(out, issue)
		case "warning":
			out.Info(issue.Message)
		}
	}

	got := buf.String()
	if strings.Contains(got, "!!! ") {
		t.Errorf(
			"error-level lint issues should use error output "+
				"(xxx prefix), not warning output (!!!): %s",
			strings.TrimSpace(got))
	}
	if !strings.Contains(got, "xxx ") {
		t.Errorf(
			"expected error output (xxx prefix) for error-level "+
				"issues, got: %s",
			strings.TrimSpace(got))
	}
}
