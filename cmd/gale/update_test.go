package main

import (
	"errors"
	"os"
	"sort"
	"strings"
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
	err := finishUpdate(false, 0, func() error {
		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("finishUpdate error = %v, want %v", err, errBoom)
	}
}

func TestFinishUpdateRebuildsWhenNothingUpdated(t *testing.T) {
	called := false
	err := finishUpdate(false, 0, func() error {
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
	err := finishUpdate(true, 0, func() error {
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

// TestFinishUpdateReturnsErrorOnFailure verifies that finishUpdate
// returns a non-nil error when one or more package installs failed,
// even if the generation rebuild itself succeeds. Without this, a
// partially-failed update exits 0, hiding failures from callers and
// CI scripts.
func TestFinishUpdateReturnsErrorOnFailure(t *testing.T) {
	err := finishUpdate(false, 1, func() error { return nil })
	if err == nil {
		t.Error("finishUpdate must return non-nil error when failed > 0")
	}
}

func TestFinishUpdateWrapsRebuildErrorOnFailure(t *testing.T) {
	rebuildErr := errors.New("generation rebuild failed")
	err := finishUpdate(false, 1, func() error { return rebuildErr })
	if err == nil {
		t.Fatal("finishUpdate must return non-nil when failed > 0 and rebuild fails")
	}
	if !errors.Is(err, rebuildErr) {
		t.Errorf("finishUpdate error %q must wrap the rebuild error via %%w", err)
	}
}

func TestTapsOfflineMode(t *testing.T) {
	tests := []struct {
		name      string
		noRefresh bool
		envVal    string
		want      bool
	}{
		{"default off", false, "", false},
		{"flag forces on", true, "", true},
		{"env=1 forces on", false, "1", true},
		{"env=0 stays off", false, "0", false},
		{"env=true stays off", false, "true", false},
		{"flag wins over env=0", true, "0", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GALE_OFFLINE", tt.envVal)
			got := tapsOfflineMode(tt.noRefresh)
			if got != tt.want {
				t.Errorf("tapsOfflineMode(%v) with GALE_OFFLINE=%q = %v, want %v",
					tt.noRefresh, tt.envVal, got, tt.want)
			}
		})
	}
}

// TestUpdatePathFlagDescriptionDoesNotSayRebuild verifies that
// the --path flag on updateCmd says "Build from a local source
// directory", not "Rebuild from a local source directory".
// The description must match install --path for consistency.
func TestUpdatePathFlagDescriptionDoesNotSayRebuild(t *testing.T) {
	f := updateCmd.Flags().Lookup("path")
	if f == nil {
		t.Fatal("updateCmd has no --path flag")
	}
	if strings.Contains(f.Usage, "Rebuild") {
		t.Errorf("updateCmd --path Usage %q must not contain "+
			"\"Rebuild\" — use \"Build from a local source "+
			"directory\" to match install --path wording",
			f.Usage)
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

// TestUpdatePathRespectsDryRun verifies that update --path --dry-run
// does not perform any real writes. Bug 0004: the --path branch fires
// before the dryRun check in update.go, so installFromLocalSource is
// called unconditionally even with --dry-run, causing a real install
// attempt (and failure). After the fix, dryRun is checked first and
// the function returns nil immediately.
// TestUpdateHasScopeFlags verifies that updateCmd registers
// --global/-g and --project/-p flags, matching every other
// mutation command (install, add, remove, sync, switch).
// Today updateCmd never registers these flags; users cannot
// explicitly target global scope while inside a project dir.
func TestUpdateHasScopeFlags(t *testing.T) {
	if updateCmd.Flags().Lookup("global") == nil {
		t.Error("update is missing --global/-g flag")
	}
	if updateCmd.Flags().Lookup("project") == nil {
		t.Error("update is missing --project/-p flag")
	}
}

func TestUpdatePathRespectsDryRun(t *testing.T) {
	// Create a temp dir with a minimal gale.toml.
	tmp := t.TempDir()
	if err := os.WriteFile(
		tmp+"/gale.toml",
		[]byte("[packages]\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	// Change working dir so newCmdContext auto-detects the project.
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(origDir) })

	// Save and restore all package-level globals we mutate.
	origDryRun := dryRun
	origUpdatePath := updatePath
	origUpdateRecipes := updateRecipes
	origUpdateNoRefresh := updateNoRefresh
	origUpdateGit := updateGit
	origUpdateRecipe := updateRecipe
	origUpdateNoInstall := updateNoInstall
	origUpdateBuild := updateBuild
	t.Cleanup(func() {
		dryRun = origDryRun
		updatePath = origUpdatePath
		updateRecipes = origUpdateRecipes
		updateNoRefresh = origUpdateNoRefresh
		updateGit = origUpdateGit
		updateRecipe = origUpdateRecipe
		updateNoInstall = origUpdateNoInstall
		updateBuild = origUpdateBuild
	})

	dryRun = true
	updatePath = "/absolutely-nonexistent-source-xyz"
	updateRecipes = ""
	updateNoRefresh = true
	updateGit = false
	updateRecipe = ""
	updateNoInstall = false
	updateBuild = false

	// With dryRun=true, RunE must return nil — no real install.
	// Before the fix: updatePath != "" causes installFromLocalSource
	// to be called without a dryRun check, which fails with a
	// "no recipe found" error, making err non-nil.
	err = updateCmd.RunE(updateCmd, []string{"testpkg"})
	if err != nil {
		t.Errorf(
			"update --path --dry-run returned error %v; "+
				"want nil (dry-run must not attempt real install)",
			err)
	}
}
