package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseMissingDepValid(t *testing.T) {
	name, repo, ok := parseMissingDep(
		"MISSING_DEP openssl openssl/openssl")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if name != "openssl" {
		t.Errorf("name = %q, want %q", name, "openssl")
	}
	if repo != "openssl/openssl" {
		t.Errorf("repo = %q, want %q", repo, "openssl/openssl")
	}
}

func TestParseMissingDepWhitespace(t *testing.T) {
	name, repo, ok := parseMissingDep(
		"  MISSING_DEP zlib madler/zlib  \n")
	if !ok {
		t.Fatal("expected ok=true for trimmed input")
	}
	if name != "zlib" || repo != "madler/zlib" {
		t.Errorf("got name=%q repo=%q", name, repo)
	}
}

func TestParseMissingDepWithPreamble(t *testing.T) {
	response := "Since meson doesn't exist as a recipe, " +
		"I need to report:\n\n" +
		"MISSING_DEP meson mesonbuild/meson\n"
	name, repo, ok := parseMissingDep(response)
	if !ok {
		t.Fatal("expected ok=true for preamble response")
	}
	if name != "meson" || repo != "mesonbuild/meson" {
		t.Errorf("got name=%q repo=%q", name, repo)
	}
}

func TestParseMissingDepWrongFormat(t *testing.T) {
	_, _, ok := parseMissingDep("some other response")
	if ok {
		t.Error("expected ok=false for non-MISSING_DEP")
	}
}

func TestParseMissingDepTooFewFields(t *testing.T) {
	_, _, ok := parseMissingDep("MISSING_DEP openssl")
	if ok {
		t.Error("expected ok=false for too few fields")
	}
}

func TestParseMissingDepEmpty(t *testing.T) {
	_, _, ok := parseMissingDep("")
	if ok {
		t.Error("expected ok=false for empty string")
	}
}

func TestParseMissingDepValidatesRepo(t *testing.T) {
	tests := []struct {
		input string
		ok    bool
		desc  string
	}{
		{
			"MISSING_DEP openssl openssl/openssl",
			true, "valid owner/repo",
		},
		{
			"MISSING_DEP foo ../../etc/passwd",
			false, "path traversal",
		},
		{
			"MISSING_DEP foo owner/repo; rm -rf /",
			false, "command injection",
		},
		{
			"MISSING_DEP foo https://evil.com/payload",
			false, "URL instead of owner/repo",
		},
		{
			"MISSING_DEP foo owner/repo/extra",
			false, "too many slashes",
		},
		{
			"MISSING_DEP foo /repo",
			false, "missing owner",
		},
		{
			"MISSING_DEP foo owner/",
			false, "missing repo name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			_, _, ok := parseMissingDep(tt.input)
			if ok != tt.ok {
				t.Errorf("parseMissingDep(%q) ok = %v, want %v",
					tt.input, ok, tt.ok)
			}
		})
	}
}

func TestBuildRecipeCheckerLocalFound(t *testing.T) {
	dir := t.TempDir()
	letter := filepath.Join(dir, "o")
	if err := os.MkdirAll(letter, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(letter, "openssl.toml")
	if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}

	checker := buildRecipeChecker(dir)
	if !checker("openssl") {
		t.Error("expected true for local recipe")
	}
}

func TestBuildRecipeCheckerLocalNotFound(t *testing.T) {
	dir := t.TempDir()
	checker := buildRecipeChecker(dir)
	// "zzzznotreal" won't exist locally or in registry.
	if checker("zzzznotreal") {
		t.Error("expected false for nonexistent recipe")
	}
}

func TestBuildRecipeCheckerEmptyName(t *testing.T) {
	dir := t.TempDir()
	checker := buildRecipeChecker(dir)
	// Empty name must not panic.
	if checker("") {
		t.Error("expected false for empty name")
	}
}

func TestCheckDepCycleDetected(t *testing.T) {
	seen := map[string]bool{
		"openssl/openssl": true,
	}
	err := checkDepCycle("openssl", "openssl/openssl", seen)
	if err == nil {
		t.Fatal("expected error for cycle")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error should mention cycle, got: %v", err)
	}
}

func TestCheckDepCycleNotDetected(t *testing.T) {
	seen := map[string]bool{
		"zlib/zlib": true,
	}
	err := checkDepCycle("openssl", "openssl/openssl", seen)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	// After the check, the dep should be added to seen.
	if !seen["openssl/openssl"] {
		t.Error("expected depRepo added to seen set")
	}
}

func TestMoveRecipeEmptyName(t *testing.T) {
	dir := t.TempDir()
	// Create a file with no base name before the .toml
	// extension: ".toml" → name becomes empty string.
	src := filepath.Join(dir, ".toml")
	if err := os.WriteFile(src, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := moveRecipe(src, dir)
	if err == nil {
		t.Fatal("expected error for empty package name")
	}
}
