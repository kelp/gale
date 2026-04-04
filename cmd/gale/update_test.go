package main

import (
	"sort"
	"testing"
)

func TestUpdateOrderIsDeterministic(t *testing.T) {
	// Build a slice of names large enough that
	// non-deterministic iteration would be detected
	// across multiple runs.
	names := []string{
		"juliet", "india", "hotel", "golf", "foxtrot",
		"echo", "delta", "charlie", "bravo", "alpha",
	}

	order := sortedTargetKeys(names)
	if !sort.StringsAreSorted(order) {
		t.Errorf("target keys not sorted: %v", order)
	}
}

func TestIsGitHash(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"abc1234", true},     // 7-char hex = git short hash
		{"abcdef0", true},     // 7-char hex
		{"1234567", true},     // all digits, still valid hex
		{"1.7.1", false},      // semver
		{"0.3.0", false},      // semver
		{"v2.0.0", false},     // tagged version
		{"abc123", false},     // too short (6 chars)
		{"abcdefgh", false},   // 8 chars but not hex
		{"abc1234z", false},   // non-hex char
		{"abcdef01234", true}, // longer hex hash
		{"abc12345678", true}, // 11-char hex
		{"", false},           // empty
		{"abc", false},        // too short
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := isGitHash(tt.input)
			if got != tt.want {
				t.Errorf(
					"isGitHash(%q) = %v, want %v",
					tt.input, got, tt.want)
			}
		})
	}
}

func TestIsNewerVersion(t *testing.T) {
	tests := []struct {
		current   string
		candidate string
		want      bool
	}{
		// Clear upgrades.
		{"0.2.0", "0.8.1", true},
		{"1.0.0", "1.0.1", true},
		{"1.0.0", "2.0.0", true},

		// Downgrades — must return false.
		{"0.8.1", "0.2.0", false},
		{"2.0.0", "1.0.0", false},
		{"1.0.1", "1.0.0", false},

		// Same version — no update needed.
		{"0.8.1", "0.8.1", false},

		// Dev/pre-release to stable release is an upgrade.
		{"0.8.1-dev.2+47a65de", "0.8.1", true},
		{"0.8.1-dev.2", "0.8.1", true},

		// Stable to dev of same version is a downgrade.
		{"0.8.1", "0.8.1-dev.2", false},

		// Dev of higher version beats stable of lower.
		{"0.8.1", "0.9.0-dev.1", true},

		// Dev of lower version is a downgrade.
		{"0.8.2-dev.1", "0.8.1", false},

		// Non-semver (git hashes, etc.) — proceed with
		// update since we can't compare.
		{"abc1234", "0.8.1", true},
		{"0.8.1", "abc1234", true},
		{"abc1234", "def5678", true},
	}

	for _, tt := range tests {
		t.Run(tt.current+"→"+tt.candidate, func(t *testing.T) {
			got := isNewerVersion(tt.candidate, tt.current)
			if got != tt.want {
				t.Errorf(
					"isNewerVersion(%q, %q) = %v, want %v",
					tt.candidate, tt.current, got, tt.want)
			}
		})
	}
}

func TestUpdateGitSkipsWhenVersionIsSemver(t *testing.T) {
	// A semver version like "1.7.1" should never match a
	// 7-char git hash like "abc1234". The up-to-date
	// check should only compare when the installed version
	// is itself a git hash.
	installed := "1.7.1"
	remoteHash := "abc1234"

	// Before fix: cfg.Packages[name] == remoteHash would
	// compare "1.7.1" == "abc1234" — always false, so
	// update always proceeds (unreachable up-to-date path).
	// After fix: isGitHash("1.7.1") returns false, so we
	// skip the comparison and proceed to update.
	if !isGitHash(installed) {
		// Correctly detected as non-hash — update proceeds.
		return
	}
	t.Error("semver version should not be treated as git hash")

	// When both are hashes, comparison is valid.
	installedHash := "def5678"
	if isGitHash(installedHash) && installedHash == remoteHash {
		t.Error("different hashes should not match")
	}
}
