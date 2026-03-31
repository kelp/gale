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
