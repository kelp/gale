package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildCmdHasRecipesFlag(t *testing.T) {
	f := buildCmd.Flags().Lookup("recipes")
	if f == nil {
		t.Fatal("build command should have --recipes flag")
	}
	// No bare form: NoOptDefVal must be unset so the space
	// form (--recipes <dir>) parses (gh#114).
	if f.NoOptDefVal != "" {
		t.Errorf("recipes flag NoOptDefVal = %q, want empty",
			f.NoOptDefVal)
	}
}

func TestBuildCmdHasOutputFlag(t *testing.T) {
	f := buildCmd.Flags().Lookup("output")
	if f == nil {
		t.Fatal("build command should have --output flag")
	}
	if f.Shorthand != "o" {
		t.Errorf("output flag shorthand = %q, want %q",
			f.Shorthand, "o")
	}
}

func TestResolveBuildOutputDirDefaultsToCwd(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	got, err := resolveBuildOutputDir("")
	if err != nil {
		t.Fatalf("resolveBuildOutputDir(\"\") error: %v", err)
	}
	if got != cwd {
		t.Errorf("resolveBuildOutputDir(\"\") = %q, want cwd %q",
			got, cwd)
	}
}

func TestResolveBuildOutputDirUsesExplicit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out")

	got, err := resolveBuildOutputDir(dir)
	if err != nil {
		t.Fatalf("resolveBuildOutputDir(%q) error: %v", dir, err)
	}
	if got != dir {
		t.Errorf("resolveBuildOutputDir(%q) = %q, want %q",
			dir, got, dir)
	}
	// The directory must be created so the build can write into it.
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("expected output dir created: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("expected %q to be a directory", dir)
	}
}

func TestResolveBuildOutputDirLeavesCwdClean(t *testing.T) {
	// Pointing output at a scratch dir must not create any
	// artifact in the current working directory.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadDir(cwd)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := resolveBuildOutputDir(filepath.Join(t.TempDir(), "o")); err != nil {
		t.Fatal(err)
	}

	after, err := os.ReadDir(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("cwd entry count changed: before %d, after %d",
			len(before), len(after))
	}
}
