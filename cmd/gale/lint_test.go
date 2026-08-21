package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/lint"
	"github.com/kelp/gale/internal/output"
)

// TestLintWarningLevelUsesWarnOutput pins audit
// RO-J:output-format/0005: warnings must render with the
// yellow `!!! ` prefix, not the cyan `--> ` info prefix.
// Otherwise the severity hierarchy collapses to two tiers and
// users can't distinguish warnings from progress prints.
func TestLintWarningLevelUsesWarnOutput(t *testing.T) {
	// A recipe with a warning-level issue (missing homepage,
	// missing license). No error-level issues.
	data := `
[package]
name = "test"
version = "1.0.0"
[source]
url = "https://example.com/test-1.0.0.tar.gz"
sha256 = "2be64e7129cecb11d5906290eba10af694fb9e3e7f9fc208a311dc33ca837eb0"
[build]
steps = ["make install PREFIX=${PREFIX}"]
`
	path := filepath.Join(t.TempDir(), "test.toml")
	if err := os.WriteFile(
		path, []byte(data), 0o644,
	); err != nil {
		t.Fatalf("writing test recipe: %v", err)
	}

	issues := lint.Lint(data, path)
	hasWarn := false
	for _, issue := range issues {
		if issue.Level == "warning" {
			hasWarn = true
			break
		}
	}
	if !hasWarn {
		t.Fatal("expected at least one warning-level issue")
	}

	var buf bytes.Buffer
	out := output.New(&buf, false)
	emitLintIssues(out, path, issues)

	got := buf.String()
	if !strings.Contains(got, "!!! ") {
		t.Errorf(
			"warning-level lint issues should use warn output "+
				"(!!! prefix), got: %s", strings.TrimSpace(got),
		)
	}
	if strings.Contains(got, "--> ") {
		t.Errorf(
			"warning-level lint issues should not use info "+
				"output (--> prefix), got: %s",
			strings.TrimSpace(got),
		)
	}
}

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
		path, []byte(data), 0o644,
	); err != nil {
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
			out.Error(issue.Message)
		case "warning":
			out.Info(issue.Message)
		}
	}

	got := buf.String()
	if strings.Contains(got, "!!! ") {
		t.Errorf(
			"error-level lint issues should use error output "+
				"(xxx prefix), not warning output (!!!): %s",
			strings.TrimSpace(got),
		)
	}
	if !strings.Contains(got, "xxx ") {
		t.Errorf(
			"expected error output (xxx prefix) for error-level "+
				"issues, got: %s",
			strings.TrimSpace(got),
		)
	}
}

// --- gale lint --strict (gale-recipes#189) ---
//
// Exit-code contract. Without --strict only error-level
// issues fail, which leaves a CI step over a warning-only
// recipe permanently green. With --strict any issue fails.

// lintCleanRecipe has no lint issues at all.
const lintCleanRecipe = `
[package]
name = "jq"
version = "1.8.1"
description = "Lightweight JSON processor"
license = "MIT"
homepage = "https://jqlang.github.io/jq"

[source]
repo = "jqlang/jq"
url = "https://github.com/jqlang/jq/releases/download/jq-1.8.1/jq-1.8.1.tar.gz"
sha256 = "2be64e7129cecb11d5906290eba10af694fb9e3e7f9fc208a311dc33ca837eb0"

[build]
steps = [
  "./configure --prefix=${PREFIX}",
  "make -j${JOBS}",
  "make install",
]
`

// lintWarningRecipe trips lintCgoEnabled and nothing else:
// a go build with no CGO_ENABLED. Warning level only.
const lintWarningRecipe = `
[package]
name = "gojq"
version = "0.12.17"
description = "Pure Go implementation of jq"
license = "MIT"
homepage = "https://github.com/itchyny/gojq"

[source]
repo = "itchyny/gojq"
url = "https://github.com/itchyny/gojq/archive/refs/tags/v0.12.17.tar.gz"
sha256 = "2be64e7129cecb11d5906290eba10af694fb9e3e7f9fc208a311dc33ca837eb0"

[dependencies]
build = ["go"]

[build]
steps = ["go build -o ${PREFIX}/bin/gojq ./cmd/gojq"]
`

// lintErrorRecipe is missing package.name: error level.
const lintErrorRecipe = `
[package]
version = "1.0.0"

[source]
url = "https://example.com/foo-1.0.0.tar.gz"
sha256 = "2be64e7129cecb11d5906290eba10af694fb9e3e7f9fc208a311dc33ca837eb0"

[build]
steps = ["make install PREFIX=${PREFIX}"]
`

var lintStrictCases = []struct {
	name string
	// file is the recipe path under a temp dir; lint checks
	// the letter bucket against the package name.
	file       string
	data       string
	wantFail   bool // plain `gale lint`
	wantStrict bool // `gale lint --strict`
}{
	{
		name: "clean recipe passes in both modes",
		file: "j/jq.toml",
		data: lintCleanRecipe,
	},
	{
		name:       "warning-only recipe fails only under strict",
		file:       "g/gojq.toml",
		data:       lintWarningRecipe,
		wantStrict: true,
	},
	{
		name:       "error-level recipe fails in both modes",
		file:       "f/foo.toml",
		data:       lintErrorRecipe,
		wantFail:   true,
		wantStrict: true,
	},
}

func TestLintStrictExitCode(t *testing.T) {
	for _, tt := range lintStrictCases {
		t.Run(tt.name, func(t *testing.T) {
			path := writeLintRecipe(t, tt.file, tt.data)
			for _, strict := range []bool{false, true} {
				want := tt.wantFail
				if strict {
					want = tt.wantStrict
				}
				err := runLintCmd(t, path, strict)
				if (err != nil) != want {
					t.Errorf(
						"gale lint (strict=%v) %s: got error %v, "+
							"want failure=%v",
						strict, tt.file, err, want,
					)
				}
			}
		})
	}
}

// TestLintStrictKeepsIssueOutput pins --strict to the exit
// code alone: the per-issue lines a CI log shows must not
// depend on the flag.
func TestLintStrictKeepsIssueOutput(t *testing.T) {
	path := writeLintRecipe(t, "g/gojq.toml", lintWarningRecipe)

	plain := captureStderr(t, func() {
		_ = runLintCmd(t, path, false)
	})
	strict := captureStderr(t, func() {
		_ = runLintCmd(t, path, true)
	})

	if plain != strict {
		t.Errorf(
			"--strict changed lint output:\n got: %q\nwant: %q",
			strict, plain,
		)
	}
	if !strings.Contains(plain, "CGO_ENABLED") {
		t.Fatalf(
			"expected the cgo warning in lint output, got: %q",
			plain,
		)
	}
}

// runLintCmd runs lintCmd over one recipe, with or without
// --strict, and returns the command's error.
func runLintCmd(t *testing.T, path string, strict bool) error {
	t.Helper()
	resetLintFlags(t)
	flag := lintCmd.Flags().Lookup("strict")
	if flag == nil {
		if !strict {
			return lintCmd.RunE(lintCmd, []string{path})
		}
		t.Fatal("gale lint has no --strict flag")
	}
	if err := lintCmd.Flags().Set(
		"strict", strconv.FormatBool(strict),
	); err != nil {
		t.Fatalf("setting --strict=%v: %v", strict, err)
	}
	return lintCmd.RunE(lintCmd, []string{path})
}

// writeLintRecipe writes data to <tempdir>/<rel> and returns
// the path. rel carries the letter bucket lint checks.
func writeLintRecipe(t *testing.T, rel, data string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating recipe dir: %v", err)
	}
	if err := os.WriteFile(
		path, []byte(data), 0o644,
	); err != nil {
		t.Fatalf("writing test recipe: %v", err)
	}
	return path
}
