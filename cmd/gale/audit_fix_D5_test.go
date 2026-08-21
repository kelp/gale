package main

// Tests for the Drop .tool-versions slice. Issue #80 added
// env --project support for a .tool-versions-only tree.
// Milestone 2 reverses that: gale.toml is the only project
// manifest.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnvProjectRejectsToolVersionsOnlyProject verifies that
// `gale env --project` errors in a directory that has only a
// .tool-versions file (no gale.toml). Issue #80 accepted that
// tree as a project; this slice does not.
func TestEnvProjectRejectsToolVersionsOnlyProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	proj := filepath.Join(home, "myproject")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	// Only a .tool-versions file — no gale.toml.
	if err := os.WriteFile(
		filepath.Join(proj, ".tool-versions"),
		[]byte("jq 1.7.1\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	chdirTo(t, proj)

	var buf bytes.Buffer
	envCmd.SetOut(&buf)
	envProject = true
	envGlobal = false
	envVarsOnly = false
	t.Cleanup(func() {
		envProject = false
		envGlobal = false
		envVarsOnly = false
	})

	err := envCmd.RunE(envCmd, nil)
	if err == nil {
		t.Fatal("env --project in a .tool-versions-only dir must fail")
	}
	if err.Error() != errNoProject {
		t.Fatalf("error = %q, want %q", err.Error(), errNoProject)
	}
}

// TestEnvAutoIgnoresToolVersionsOnly verifies that `gale env`
// (auto mode, no scope flag) uses the global scope when only
// .tool-versions is present. Issue #80 resolved that tree as a
// project.
func TestEnvAutoIgnoresToolVersionsOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	proj := filepath.Join(home, "myproject")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(proj, ".tool-versions"),
		[]byte("jq 1.7.1\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	chdirTo(t, proj)

	var buf bytes.Buffer
	envCmd.SetOut(&buf)
	envProject = false
	envGlobal = false
	envVarsOnly = false
	t.Cleanup(func() {
		envProject = false
		envGlobal = false
		envVarsOnly = false
	})

	if err := envCmd.RunE(envCmd, nil); err != nil {
		t.Fatalf("envCmd.RunE (auto) in .tool-versions-only dir: %v",
			err)
	}

	output := buf.String()
	projectBin := filepath.Join(proj, ".gale", "current", "bin")
	if strings.Contains(output, projectBin) {
		t.Errorf("auto mode must not use project .gale dir %q; got:\n%s",
			projectBin, output)
	}
	wantDir := filepath.Join(home, ".gale", "current", "bin")
	if !strings.Contains(output, wantDir) {
		t.Errorf(
			"auto mode: expected PATH to include global .gale dir %q; got:\n%s",
			wantDir, output,
		)
	}
}
