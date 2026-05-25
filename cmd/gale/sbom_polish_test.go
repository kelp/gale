package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSbomReadingConfigErrorWrappedOnce pins RO-K-4: when
// `gale sbom` fails to read its config because of a non-ENOENT
// error (e.g. an unreadable file or a directory shadowing the
// expected path), the surfaced error must contain "reading
// config:" exactly once.  The original bug double-wrapped the
// inner read error in both `resolveSbomConfig` and its caller.
//
// We trigger the unreadable case by pointing the global path
// at a directory: `os.ReadFile` returns "is a directory" (not
// ENOENT) so the empty-state branch is skipped and the wrap
// path runs.
func TestSbomReadingConfigErrorWrappedOnce(t *testing.T) {
	tempHome := t.TempDir()
	galeDir := filepath.Join(tempHome, ".gale")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Create a directory at the expected gale.toml path.  Any
	// `os.ReadFile` against it surfaces a non-ENOENT IO error.
	galeConfigPath := filepath.Join(galeDir, "gale.toml")
	if err := os.Mkdir(galeConfigPath, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", tempHome)

	// Run from a dir with no project gale.toml so sbom falls
	// back to the broken global path.
	cwd := filepath.Join(tempHome, "empty")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	orig, _ := os.Getwd()
	_ = os.Chdir(cwd)
	t.Cleanup(func() { _ = os.Chdir(orig) })

	// Reset scope flags between tests.
	t.Cleanup(func() {
		sbomGlobal, sbomProject, sbomAll, sbomJSON = false, false, false, false
	})

	var stdout, stderr bytes.Buffer
	err := runSbom(&stdout, &stderr, nil)
	if err == nil {
		t.Fatal("expected error for unreadable config")
	}
	msg := err.Error()
	count := strings.Count(msg, "reading config:")
	if count != 1 {
		t.Errorf("'reading config:' appears %d times in %q, want exactly 1",
			count, msg)
	}
}

// TestOutputTableEmptyFieldsRenderPlaceholder pins RO-K-5:
// when a package's recipe lookup fails, License and SourceURL
// stay empty; the tabwriter then produces awkward two-space
// gaps in the row.  outputTable must replace empty License /
// Source / Method fields with the standard "-" placeholder so
// the column widths stay aligned.
func TestOutputTableEmptyFieldsRenderPlaceholder(t *testing.T) {
	var buf bytes.Buffer
	entries := []sbomEntry{
		{
			Name:    "jq",
			Version: "1.7.1-1",
			// License, SourceURL, Method all empty — recipe
			// resolution failed.
		},
	}
	outputTable(&buf, entries, false)
	got := buf.String()

	// Find the data row (the one starting with "jq").
	var row string
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "jq") {
			row = line
			break
		}
	}
	if row == "" {
		t.Fatalf("missing data row: %q", got)
	}

	// Each empty-but-rendered column should carry "-".  Look
	// for two distinct dashes, separated by whitespace,
	// surviving tabwriter padding.  Anything less means the
	// fix isn't routing through.
	dashCount := strings.Count(row, " - ")
	if dashCount < 2 {
		t.Errorf("row %q has %d ' - ' placeholders; want >= 2",
			row, dashCount)
	}
}

// TestOutputTablePopulatedFieldsUntouched protects the happy
// path: when License / Source / Method are real, they must
// land in the table verbatim (no spurious "-" substitution).
func TestOutputTablePopulatedFieldsUntouched(t *testing.T) {
	var buf bytes.Buffer
	entries := []sbomEntry{
		{
			Name:      "jq",
			Version:   "1.7.1",
			License:   "MIT",
			SourceURL: "https://github.com/jqlang/jq",
			Method:    "binary",
		},
	}
	outputTable(&buf, entries, false)
	got := buf.String()
	for _, want := range []string{"MIT", "github.com", "binary"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in output, got: %q", want, got)
		}
	}
}
