package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sbomTestSetup arranges a fresh HOME/cwd combination for an
// sbom run and chdirs into the cwd.
func sbomTestSetup(t *testing.T, home, projectConfig, globalConfig string) {
	t.Helper()
	t.Setenv("HOME", home)
	if globalConfig != "" {
		galeDir := filepath.Join(home, ".gale")
		if err := os.MkdirAll(galeDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(galeDir, "gale.toml"),
			[]byte(globalConfig), 0o644,
		); err != nil {
			t.Fatal(err)
		}
	}
	cwd := filepath.Join(home, "work")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	if projectConfig != "" {
		if err := os.WriteFile(
			filepath.Join(cwd, "gale.toml"),
			[]byte(projectConfig), 0o644,
		); err != nil {
			t.Fatal(err)
		}
	}
	orig, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(orig) })
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
}

func withSbomJSON(t *testing.T, v bool) {
	t.Helper()
	old := sbomJSON
	sbomJSON = v
	t.Cleanup(func() { sbomJSON = old })
}

// Finding 0005 (empty-state cluster): a truly fresh HOME with no
// ~/.gale/gale.toml at all must be treated as "nothing declared",
// exit 0, with the same empty-state message peers print.
func TestSbomNoConfigAtAll_ExitsZeroWithEmptyMessage(t *testing.T) {
	home := t.TempDir()
	sbomTestSetup(t, home, "", "")
	withSbomJSON(t, false)

	var stdout, stderr bytes.Buffer
	if err := runSbom(&stdout, &stderr, nil); err != nil {
		t.Fatalf("runSbom: %v", err)
	}
	if !strings.Contains(stderr.String(), "No packages installed.") {
		t.Errorf("stderr missing empty-state message: %q",
			stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "" {
		t.Errorf("stdout should be empty in no-config case, got: %q",
			stdout.String())
	}
}

// Finding 0005 (empty-state): same as above but for --json mode.
// Must emit "[]" rather than failing.
func TestSbomJSON_NoConfigAtAll_EmitsEmptyArray(t *testing.T) {
	home := t.TempDir()
	sbomTestSetup(t, home, "", "")
	withSbomJSON(t, true)

	var stdout, stderr bytes.Buffer
	if err := runSbom(&stdout, &stderr, nil); err != nil {
		t.Fatalf("runSbom: %v", err)
	}
	if strings.TrimSpace(stdout.String()) != "[]" {
		t.Errorf("stdout = %q, want %q",
			stdout.String(), "[]\n")
	}
}

// Finding 0005 (bad-input) and 0001 (output-format): gale.toml
// exists but has no packages — JSON must be "[]" not "null".
func TestSbomJSON_EmptyConfig_EmitsEmptyArray(t *testing.T) {
	home := t.TempDir()
	sbomTestSetup(t, home, "", "[packages]\n")
	withSbomJSON(t, true)

	var stdout, stderr bytes.Buffer
	if err := runSbom(&stdout, &stderr, nil); err != nil {
		t.Fatalf("runSbom: %v", err)
	}
	got := strings.TrimSpace(stdout.String())
	if got == "null" {
		t.Fatalf("stdout emitted JSON null on empty: %q",
			stdout.String())
	}
	if got != "[]" {
		t.Errorf("stdout = %q, want %q",
			stdout.String(), "[]\n")
	}
}

// Same empty-message contract when gale.toml exists but is empty.
func TestSbomEmptyConfig_ExitsZeroWithEmptyMessage(t *testing.T) {
	home := t.TempDir()
	sbomTestSetup(t, home, "", "[packages]\n")
	withSbomJSON(t, false)

	var stdout, stderr bytes.Buffer
	if err := runSbom(&stdout, &stderr, nil); err != nil {
		t.Fatalf("runSbom: %v", err)
	}
	if !strings.Contains(stderr.String(), "No packages installed.") {
		t.Errorf("stderr missing empty-state message: %q",
			stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "" {
		t.Errorf("stdout should be empty when no packages, got: %q",
			stdout.String())
	}
}

// Project gale.toml with no packages should also produce the
// consistent empty-state behaviour, regardless of whether a
// global config exists.
func TestSbomProjectEmptyConfig_ExitsZeroWithEmptyMessage(t *testing.T) {
	home := t.TempDir()
	sbomTestSetup(t, home, "[packages]\n",
		"[packages]\njq = \"1.8.1\"\n")
	withSbomJSON(t, false)

	var stdout, stderr bytes.Buffer
	if err := runSbom(&stdout, &stderr, nil); err != nil {
		t.Fatalf("runSbom: %v", err)
	}
	if !strings.Contains(stderr.String(), "No packages installed.") {
		t.Errorf("stderr missing empty-state message: %q",
			stderr.String())
	}
}
