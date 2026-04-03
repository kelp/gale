package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerationsCommandExists(t *testing.T) {
	for _, c := range rootCmd.Commands() {
		if c.Name() == "generations" {
			return
		}
	}
	t.Fatal("generations command not found on rootCmd")
}

func TestGenerationsDiffSubcommand(t *testing.T) {
	var found bool
	for _, c := range rootCmd.Commands() {
		if c.Name() == "generations" {
			for _, sub := range c.Commands() {
				if sub.Name() == "diff" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("diff subcommand not found on generations")
	}
}

func TestGenerationsRollbackSubcommand(t *testing.T) {
	var found bool
	for _, c := range rootCmd.Commands() {
		if c.Name() == "generations" {
			for _, sub := range c.Commands() {
				if sub.Name() == "rollback" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("rollback subcommand not found on generations")
	}
}

func TestGenRollbackRejectsZeroAndNegative(t *testing.T) {
	// Set up a temp project with a gale.toml and a
	// current generation so resolveGaleDir and
	// generation.Current succeed.
	projDir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(projDir, "gale.toml"),
		[]byte("[packages]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	galeDir := filepath.Join(projDir, ".gale")
	genDir := filepath.Join(galeDir, "gen", "1", "bin")
	if err := os.MkdirAll(genDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Join("gen", "1"),
		filepath.Join(galeDir, "current")); err != nil {
		t.Fatal(err)
	}

	// Change to the project directory so resolveGaleDir
	// finds the project config.
	orig, _ := os.Getwd()
	os.Chdir(projDir)
	t.Cleanup(func() { os.Chdir(orig) })

	tests := []struct {
		arg  string
		want string
	}{
		{"0", "generation number must be positive"},
		{"-1", "generation number must be positive"},
		{"-99", "generation number must be positive"},
	}

	for _, tt := range tests {
		t.Run(tt.arg, func(t *testing.T) {
			err := genRollbackCmd.RunE(
				genRollbackCmd, []string{tt.arg})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to contain %q",
					err.Error(), tt.want)
			}
		})
	}
}
