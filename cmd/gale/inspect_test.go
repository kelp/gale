package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/inspect"
	"github.com/kelp/gale/internal/output"
)

// TestPrintHumanIssuesTallyHonoursOutputHelper pins RO-K-3:
// the trailing "N issue(s) across M package(s)" tally must be
// routed through the *output.Output helper (which respects
// --no-color, --plain, --quiet) instead of bypassing it with
// a raw fmt.Fprintf to os.Stderr. The data lines stay on
// stdout via fmt.Println; only the tally gates on color.
func TestPrintHumanIssuesTallyHonoursOutputHelper(t *testing.T) {
	issues := []inspect.Issue{
		{
			Package: "jq",
			Version: "1.7.1-1",
			Binary:  "bin/jq",
			Kind:    inspect.KindUnresolvableRef,
			Details: "@rpath/libfoo.dylib",
		},
	}
	scanned := []target{{name: "jq", version: "1.7.1-1"}}

	var stderr bytes.Buffer
	// Color ON: every line written via the helper carries an
	// ANSI escape; a raw Fprintf has none.  This lets the test
	// detect a bypass without parsing prefixes.
	out := output.NewWithOptions(&stderr, output.Options{
		Color: true, Steps: true,
	})

	printHumanIssuesTo(out, issues, scanned)

	got := stderr.String()
	if !strings.Contains(got, "1 issue") {
		t.Fatalf("tally missing from stderr: %q", got)
	}
	// Find the tally line specifically.
	var tally string
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "1 issue") {
			tally = line
			break
		}
	}
	if tally == "" {
		t.Fatalf("no tally line in output: %q", got)
	}
	// The output helper prefixes status lines with "--> ",
	// "==> ", "!!! ", or "xxx ". Assert one of those is
	// present so the test fails if someone reverts to a raw
	// Fprintf.
	if !strings.Contains(tally, "--> ") &&
		!strings.Contains(tally, "==> ") &&
		!strings.Contains(tally, "!!! ") &&
		!strings.Contains(tally, "xxx ") {
		t.Errorf("tally bypasses output helper (no prefix glyph): %q",
			tally)
	}
}

// TestPrintHumanIssuesNoIssuesUsesSuccess keeps the existing
// happy-path contract: zero issues → a single success line via
// the helper.  Locking this in so the refactor below doesn't
// accidentally drop it.
func TestPrintHumanIssuesNoIssuesUsesSuccess(t *testing.T) {
	var stderr bytes.Buffer
	out := output.NewWithOptions(&stderr, output.Options{})
	printHumanIssuesTo(out, nil, []target{
		{name: "jq", version: "1.7.1-1"},
	})
	got := stderr.String()
	if !strings.Contains(got, "no issues") {
		t.Errorf("expected 'no issues' summary, got: %q", got)
	}
}
