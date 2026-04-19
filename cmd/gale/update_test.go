package main

import (
	"errors"
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

		// Revision bumps: -N suffix after the patch part is a
		// gale revision (newer), not a semver pre-release (older).
		// Struct fields are {current, candidate, want}.
		{"1.2.3", "1.2.3-2", true},    // bare (rev 1) → rev 2 upgrade
		{"1.2.3-2", "1.2.3", false},   // rev 2 → bare is downgrade
		{"1.2.3-2", "1.2.3-3", true},  // rev 2 → rev 3 upgrade
		{"1.2.3-3", "1.2.3-2", false}, // rev 3 → rev 2 downgrade
		{"1.2.3-2", "1.2.3-2", false}, // equal
		{"1.2.3-2", "1.2.4", true},    // patch bump beats revision
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

func TestUpdateAction(t *testing.T) {
	tests := []struct {
		name      string
		candidate string
		current   string
		inStore   bool
		wantVer   string
		wantSkip  bool
	}{
		{
			name:      "same version and in store skips",
			candidate: "1.0.0", current: "1.0.0",
			inStore: true, wantVer: "1.0.0", wantSkip: true,
		},
		{
			name:      "same version but missing from store reinstalls",
			candidate: "1.0.0", current: "1.0.0",
			inStore: false, wantVer: "1.0.0", wantSkip: false,
		},
		{
			name:      "newer version upgrades",
			candidate: "2.0.0", current: "1.0.0",
			inStore: true, wantVer: "2.0.0", wantSkip: false,
		},
		{
			name:      "newer version upgrades even if missing",
			candidate: "2.0.0", current: "1.0.0",
			inStore: false, wantVer: "2.0.0", wantSkip: false,
		},
		{
			name:      "older registry version reinstalls current",
			candidate: "0.9.0", current: "1.0.0",
			inStore: false, wantVer: "1.0.0", wantSkip: false,
		},
		{
			// Recipe bumped from revision 1 (bare "1.0.0") to
			// revision 2 ("1.0.0-2"). Installed version is the
			// bare pre-revision dir, but the store's bidirectional
			// resolver makes IsInstalled report true via back-compat.
			// Without revision-aware comparison, update skipped
			// here and users stayed on the old binary.
			name:      "revision bump triggers reinstall",
			candidate: "1.0.0-2", current: "1.0.0",
			inStore: true, wantVer: "1.0.0-2", wantSkip: false,
		},
		{
			name:      "revision bump to rev 3 from rev 2",
			candidate: "1.0.0-3", current: "1.0.0-2",
			inStore: true, wantVer: "1.0.0-3", wantSkip: false,
		},
		{
			name:      "same revision skips",
			candidate: "1.0.0-2", current: "1.0.0-2",
			inStore: true, wantVer: "1.0.0-2", wantSkip: true,
		},
		{
			name:      "lower revision does not downgrade",
			candidate: "1.0.0", current: "1.0.0-2",
			inStore: true, wantVer: "1.0.0-2", wantSkip: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ver, skip := updateAction(
				tt.candidate, tt.current, tt.inStore)
			if ver != tt.wantVer {
				t.Errorf("version = %q, want %q",
					ver, tt.wantVer)
			}
			if skip != tt.wantSkip {
				t.Errorf("skip = %v, want %v",
					skip, tt.wantSkip)
			}
		})
	}
}

func TestFinishUpdateReturnsRebuildError(t *testing.T) {
	errBoom := errors.New("boom")
	err := finishUpdate(false, func() error {
		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("finishUpdate error = %v, want %v", err, errBoom)
	}
}

func TestFinishUpdateRebuildsWhenNothingUpdated(t *testing.T) {
	called := false
	err := finishUpdate(false, func() error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("finishUpdate error = %v, want nil", err)
	}
	if !called {
		t.Fatal("rebuild should be called when nothing updated")
	}
}

func TestFinishUpdateSkipsRebuildInDryRun(t *testing.T) {
	called := false
	err := finishUpdate(true, func() error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("finishUpdate error = %v, want nil", err)
	}
	if called {
		t.Fatal("rebuild should not be called in dry-run mode")
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
