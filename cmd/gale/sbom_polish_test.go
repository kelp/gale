package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSbomReadingConfigErrorSurfacedOnce pins RO-K-4: when
// `gale sbom` fails to read its config because of a non-ENOENT
// error (e.g. a directory shadowing the expected path), the
// surfaced error must mention the config path exactly once.
// The original bug double-wrapped the inner read error in both
// `resolveSbomConfig` and its caller; after `resolveSbomConfig`
// was deleted, the single error comes from collectSbomEntries.
//
// We trigger the unreadable case by pointing the global path
// at a directory: `os.ReadFile` returns "is a directory" (not
// ENOENT) so the empty-state branch is skipped and the error
// path runs.
func TestSbomReadingConfigErrorSurfacedOnce(t *testing.T) {
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
	// The error must mention the config path (not be silently
	// swallowed) and must not mention "reading config:" which
	// was the old double-wrap prefix that is no longer present.
	if !strings.Contains(msg, "gale.toml") {
		t.Errorf("error %q does not mention gale.toml", msg)
	}
	if strings.Count(msg, "reading config:") > 0 {
		t.Errorf("error %q must not contain old double-wrap prefix 'reading config:'", msg)
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

// TestSbomToolVersionsProject pins the Bugbot finding on the
// scope-resolution consolidation: a `.tool-versions`-only tree
// counts as a project (projectConfigPath returns its would-be
// gale.toml path), so sbom must read the .tool-versions
// fallback like `list` and `env` do — not bail on the missing
// gale.toml and emit an empty SBOM.
func TestSbomToolVersionsProject(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	proj := filepath.Join(tempHome, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(proj, ".tool-versions"),
		[]byte("jq 1.7.0\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	_ = os.Chdir(proj)
	t.Cleanup(func() { _ = os.Chdir(orig) })
	t.Cleanup(func() {
		sbomGlobal, sbomProject, sbomAll, sbomJSON = false, false, false, false
	})

	var stdout, stderr bytes.Buffer
	if err := runSbom(&stdout, &stderr, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "jq") || !strings.Contains(out, "1.7.0") {
		t.Errorf("sbom output missing jq 1.7.0 from .tool-versions:\nstdout: %q\nstderr: %q",
			out, stderr.String())
	}
}

// TestSbomV1MethodComesFromTheLock pins that the enforced schema is
// authoritative about how a package's bytes were produced. The legacy
// schema recorded no method, so sbom inferred one by comparing the
// recorded hash against the current recipe's binary hash. That
// inference is a guess, and once the recipe moves it is a wrong guess:
// a locked binary reports as source, or the reverse.
//
// No recipe resolves in this test, so the inference cannot fire at all
// and the default "source" stands. That is exactly what makes it a
// clean test of precedence: only reading the locked method produces
// "binary" here.
func TestSbomV1MethodComesFromTheLock(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	proj := filepath.Join(tempHome, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "gale.toml"),
		[]byte("[packages]\n  jq = \"1.8.1\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "gale.lock"),
		[]byte(v1LockMethodFixture), 0o644); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	_ = os.Chdir(proj)
	t.Cleanup(func() { _ = os.Chdir(orig) })
	sbomJSON = true
	t.Cleanup(func() {
		sbomGlobal, sbomProject, sbomAll, sbomJSON = false, false, false, false
	})

	var stdout, stderr bytes.Buffer
	if err := runSbom(&stdout, &stderr, nil); err != nil {
		t.Fatalf("runSbom: %v", err)
	}

	var entries []sbomEntry
	if err := json.Unmarshal(stdout.Bytes(), &entries); err != nil {
		t.Fatalf("decode sbom json %q: %v", stdout.String(), err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1: %+v", len(entries), entries)
	}
	if entries[0].Method != "binary" {
		t.Errorf("Method = %q, want the locked \"binary\"", entries[0].Method)
	}
	if entries[0].ArchiveSHA256 != "aaaa" {
		t.Errorf("ArchiveSHA256 = %q, want aaaa", entries[0].ArchiveSHA256)
	}
}

// v1LockMethodFixture locks jq as a binary artifact for every platform
// the test may run on, so the assertion is about precedence rather than
// about which machine ran it.
const v1LockMethodFixture = `version = 1

[packages."!gale-lock-v1"]
version = 1

[targets.default]
roots = ["jq@1.8.1-1"]

[packages."jq@1.8.1-1".artifacts."darwin/arm64"]
sha256 = "aaaa"
method = "binary"
graph_digest = "sha256:cccc"

[packages."jq@1.8.1-1".artifacts."darwin/amd64"]
sha256 = "aaaa"
method = "binary"
graph_digest = "sha256:cccc"

[packages."jq@1.8.1-1".artifacts."linux/amd64"]
sha256 = "aaaa"
method = "binary"
graph_digest = "sha256:cccc"

[packages."jq@1.8.1-1".artifacts."linux/arm64"]
sha256 = "aaaa"
method = "binary"
graph_digest = "sha256:cccc"
`
