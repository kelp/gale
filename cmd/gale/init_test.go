package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteIfNotExistsCreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.txt")

	if err := writeIfNotExists(path, "hello\n"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	if string(data) != "hello\n" {
		t.Errorf("content = %q, want %q",
			string(data), "hello\n")
	}
}

func TestWriteIfNotExistsSkipsExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.txt")

	if err := os.WriteFile(path,
		[]byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := writeIfNotExists(path, "new"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original" {
		t.Errorf("content = %q, want %q",
			string(data), "original")
	}
}

func TestAppendToGitignoreAddsLine(t *testing.T) {
	dir := t.TempDir()

	if err := appendToGitignore(dir, ".gale/"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), ".gale/") {
		t.Errorf(".gitignore should contain .gale/, got %q",
			string(data))
	}
}

func TestAppendToGitignoreSkipsDuplicate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")

	if err := os.WriteFile(path,
		[]byte(".gale/\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := appendToGitignore(dir, ".gale/"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), ".gale/") != 1 {
		t.Errorf("expected exactly one .gale/ entry, got %q",
			string(data))
	}
}

func TestAppendToGitignoreAppendsToExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")

	if err := os.WriteFile(path,
		[]byte("node_modules/\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := appendToGitignore(dir, ".gale/"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "node_modules/") {
		t.Error("existing content should be preserved")
	}
	if !strings.Contains(string(data), ".gale/") {
		t.Error(".gale/ should be appended")
	}
}
