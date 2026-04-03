package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvVarsUseShellQuoting(t *testing.T) {
	// Go's %q produces Go-syntax escape sequences
	// (e.g. \t for tab) which POSIX sh doesn't
	// understand. Vars must be single-quoted for
	// shell safety.
	dir := t.TempDir()
	configPath := filepath.Join(dir, "gale.toml")
	if err := os.WriteFile(configPath, []byte(
		"[packages]\n\n[vars]\nFOO = \"hello world\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })
	os.Chdir(dir)

	var buf bytes.Buffer
	envCmd.SetOut(&buf)
	envVarsOnly = true
	t.Cleanup(func() { envVarsOnly = false })

	if err := envCmd.RunE(envCmd, nil); err != nil {
		t.Fatalf("envCmd.RunE: %v", err)
	}

	output := buf.String()
	// Must use single quotes, not Go %q double quotes.
	if !strings.Contains(output,
		"export FOO='hello world'") {
		t.Errorf(
			"expected single-quoted export, got:\n%s",
			output)
	}
}

func TestEnvVarsEscapeEmbeddedSingleQuotes(t *testing.T) {
	// Values with embedded single quotes must be escaped
	// using the '\'' idiom.
	dir := t.TempDir()
	configPath := filepath.Join(dir, "gale.toml")
	if err := os.WriteFile(configPath, []byte(
		"[packages]\n\n[vars]\nMSG = \"it's fine\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })
	os.Chdir(dir)

	var buf bytes.Buffer
	envCmd.SetOut(&buf)
	envVarsOnly = true
	t.Cleanup(func() { envVarsOnly = false })

	if err := envCmd.RunE(envCmd, nil); err != nil {
		t.Fatalf("envCmd.RunE: %v", err)
	}

	output := buf.String()
	// The single quote in "it's" must be escaped.
	want := "export MSG='it'\\''s fine'"
	if !strings.Contains(output, want) {
		t.Errorf("expected %q in output, got:\n%s",
			want, output)
	}
}
