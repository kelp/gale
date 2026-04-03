package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrependPATHReplacesDuplicateEntry(t *testing.T) {
	// prependPATH must replace the existing PATH entry,
	// not append a second one. If two PATH entries exist,
	// getenv(3) returns the first (original) one, making
	// the gale bin dir invisible.
	env := prependPATH("/gale/bin")

	var pathEntries []string
	for _, entry := range env {
		if strings.HasPrefix(entry, "PATH=") {
			pathEntries = append(pathEntries, entry)
		}
	}

	if len(pathEntries) != 1 {
		t.Fatalf("expected 1 PATH entry, got %d: %v",
			len(pathEntries), pathEntries)
	}
	if !strings.HasPrefix(pathEntries[0], "PATH=/gale/bin:") {
		t.Errorf("PATH should start with /gale/bin: got %q",
			pathEntries[0])
	}
}

func TestSyncIfNeededUsesProjectDir(t *testing.T) {
	// When projectDir is specified, syncIfNeeded must
	// look for gale.toml there instead of os.Getwd().
	// Create a project dir with an invalid config to
	// verify it reads from projectDir (not cwd).
	projDir := t.TempDir()
	configPath := filepath.Join(projDir, "gale.toml")
	if err := os.WriteFile(configPath,
		[]byte("this is not valid toml {{{\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	// cwd has no gale.toml — if syncIfNeeded ignores
	// projectDir it would silently return.
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })
	emptyDir := t.TempDir()
	os.Chdir(emptyDir)

	var buf bytes.Buffer
	syncIfNeeded(&buf, projDir)

	if buf.Len() == 0 {
		t.Error("syncIfNeeded should read config from " +
			"projectDir, got no output")
	}
}

func TestSyncIfNeededWarnsOnBadConfig(t *testing.T) {
	// When gale.toml exists but contains invalid TOML,
	// syncIfNeeded must write a warning rather than
	// silently swallowing the error.
	dir := t.TempDir()
	configPath := filepath.Join(dir, "gale.toml")
	if err := os.WriteFile(configPath,
		[]byte("this is not valid toml {{{\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	// Change to the directory so syncIfNeeded finds
	// the config.
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })
	os.Chdir(dir)

	var buf bytes.Buffer
	syncIfNeeded(&buf, "")

	if buf.Len() == 0 {
		t.Error("syncIfNeeded should warn on invalid " +
			"config, got no output")
	}
}
