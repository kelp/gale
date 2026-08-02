package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGaleDirForConfigGlobalPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	globalConfig := filepath.Join(home, ".gale", "gale.toml")

	got, err := galeDirForConfig(globalConfig)
	if err != nil {
		t.Fatalf("galeDirForConfig: %v", err)
	}
	want := filepath.Join(home, ".gale")
	if got != want {
		t.Errorf("galeDirForConfig(%q) = %q, want %q",
			globalConfig, got, want)
	}
}

func TestGaleDirForConfigProjectPath(t *testing.T) {
	projDir := t.TempDir()
	configPath := filepath.Join(projDir, "gale.toml")

	got, err := galeDirForConfig(configPath)
	if err != nil {
		t.Fatalf("galeDirForConfig: %v", err)
	}
	want := filepath.Join(projDir, ".gale")
	if got != want {
		t.Errorf("galeDirForConfig(%q) = %q, want %q",
			configPath, got, want)
	}
}

func TestGaleDirForConfigProjectNeverReturnsGlobal(t *testing.T) {
	// A project config path must never return the global
	// gale dir, even if the project is in a subdirectory
	// of the home directory.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	globalDir := filepath.Join(home, ".gale")

	projDir := t.TempDir()
	configPath := filepath.Join(projDir, "gale.toml")

	got, err := galeDirForConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if got == globalDir {
		t.Errorf("project config returned global galeDir %q",
			got)
	}
}

// sameDir must answer for paths that do not exist yet. EvalSymlinks
// fails on a missing path, and falling back to raw string comparison
// makes /var/... and /private/var/... compare unequal on macOS even
// when they name the same place.
//
// This is not a test-only concern. The initiating scope's .gale
// directory does not exist until its first sync, and design §13's
// migration veto exempts the initiating scope by comparing exactly
// that path. A false negative there makes the scope veto itself,
// which is the deadlock §13 exists to avoid.
func TestSameDirResolvesPathsThatDoNotExistYet(t *testing.T) {
	raw := t.TempDir()
	resolved, err := filepath.EvalSymlinks(raw)
	if err != nil {
		t.Fatal(err)
	}
	if resolved == raw {
		t.Skip("temp dir is not behind a symlink on this platform")
	}

	// Neither spelling exists on disk; both name the same future dir.
	if !sameDir(
		filepath.Join(raw, ".gale"),
		filepath.Join(resolved, ".gale"),
	) {
		t.Errorf("sameDir(%q, %q) = false, want true",
			filepath.Join(raw, ".gale"),
			filepath.Join(resolved, ".gale"))
	}
}

// Different directories stay different, so the resolution above does
// not collapse unrelated paths.
func TestSameDirStillSeparatesDifferentDirs(t *testing.T) {
	base := t.TempDir()
	if sameDir(filepath.Join(base, "a"), filepath.Join(base, "b")) {
		t.Error("sameDir must not equate different directories")
	}
}
