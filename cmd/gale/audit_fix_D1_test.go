package main

// Tests for issue #62: gale verify builds OCI tag with revision
// suffix, but GHCR manifests are tagged with bare version.
//
// The lockfile stores versions in canonical "<version>-<revision>"
// form (e.g. "1.8.1-4"). GHCR tags are "<version>-<platform>"
// (e.g. "1.8.1-linux-amd64"). verify.go must strip the revision
// suffix before constructing the OCI tag.

import (
	"testing"
)

// TestBareVersion confirms that bareVersion strips numeric Debian-style
// revision suffixes and preserves non-numeric ones, matching
// internal/version.splitRevision semantics.
func TestBareVersion(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		// Numeric revision suffix — strip it.
		{"1.8.1-4", "1.8.1"},
		{"0.10.0-2", "0.10.0"},
		{"1.7.12-2", "1.7.12"},
		{"0.10.0-1", "0.10.0"},
		// No revision suffix — leave unchanged.
		{"1.8.1", "1.8.1"},
		{"0.10.0", "0.10.0"},
		// Non-numeric suffix — leave unchanged (pre-release, not
		// a revision).
		{"1.0-rc1", "1.0-rc1"},
		{"1.2.3-dev.2", "1.2.3-dev.2"},
		// Zero suffix — not a positive revision; leave unchanged.
		{"1.2-0", "1.2-0"},
	}
	for _, tc := range cases {
		got := bareVersion(tc.input)
		if got != tc.want {
			t.Errorf("bareVersion(%q) = %q, want %q",
				tc.input, got, tc.want)
		}
	}
}
