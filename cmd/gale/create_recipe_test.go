package main

import (
	"os"
	"path/filepath"
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
