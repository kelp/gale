package main

import (
	"strings"
	"testing"
)

// TestFormatOutdatedFallsBackToASCIIArrowUnderPOSIX pins
// RO-K-2.  The Unicode arrow (→) renders as "?" or garbage on
// terminals running under LANG=C / LC_ALL=C.  formatOutdated
// must detect non-UTF-8 locale and emit "->" instead.
func TestFormatOutdatedFallsBackToASCIIArrowUnderPOSIX(t *testing.T) {
	t.Setenv("LC_ALL", "C")
	t.Setenv("LANG", "C")

	items := []outdatedItem{{Name: "jq", Current: "1.7", Latest: "1.8"}}
	lines := formatOutdated(items)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	if strings.Contains(lines[0], "→") {
		t.Errorf("expected ASCII arrow under LC_ALL=C, got: %q",
			lines[0])
	}
	if !strings.Contains(lines[0], "->") {
		t.Errorf("expected '->' under LC_ALL=C, got: %q", lines[0])
	}
}

// TestFormatOutdatedKeepsUnicodeArrowUnderUTF8 confirms the
// default rendering still uses the Unicode arrow when the
// locale advertises UTF-8.  We don't want to regress the
// modern terminal output to ASCII.
func TestFormatOutdatedKeepsUnicodeArrowUnderUTF8(t *testing.T) {
	t.Setenv("LC_ALL", "en_US.UTF-8")
	t.Setenv("LANG", "en_US.UTF-8")

	items := []outdatedItem{{Name: "jq", Current: "1.7", Latest: "1.8"}}
	lines := formatOutdated(items)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "→") {
		t.Errorf("expected Unicode arrow under UTF-8 locale, got: %q",
			lines[0])
	}
}

// TestSupportsUnicodeRecognisesCommonShapes documents the
// locale heuristic used by formatOutdated.  Anything with a
// case-insensitive UTF-8 charset suffix counts as Unicode-safe;
// the bare POSIX names "C" and "POSIX" do not.  Unset all
// locale vars → behave like C (ASCII).
func TestSupportsUnicodeRecognisesCommonShapes(t *testing.T) {
	utf8 := []struct{ lcAll, lang string }{
		{"en_US.UTF-8", ""},
		{"", "en_US.UTF-8"},
		{"", "C.UTF-8"},
		{"", "en_GB.utf8"},
		{"de_DE.utf-8", ""},
	}
	for _, tc := range utf8 {
		t.Setenv("LC_ALL", tc.lcAll)
		t.Setenv("LANG", tc.lang)
		if !supportsUnicode() {
			t.Errorf("expected UTF-8 for LC_ALL=%q LANG=%q",
				tc.lcAll, tc.lang)
		}
	}

	posix := []struct{ lcAll, lang string }{
		{"C", ""},
		{"POSIX", ""},
		{"", "C"},
		{"", "POSIX"},
		{"", ""},
	}
	for _, tc := range posix {
		t.Setenv("LC_ALL", tc.lcAll)
		t.Setenv("LANG", tc.lang)
		if supportsUnicode() {
			t.Errorf("expected ASCII for LC_ALL=%q LANG=%q",
				tc.lcAll, tc.lang)
		}
	}
}
