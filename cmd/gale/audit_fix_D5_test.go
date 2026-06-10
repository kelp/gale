package main

// Tests for issue #80: gale env --project rejects a project that has
// only a .tool-versions file, even though other read-only --project
// commands accept it via projectConfigPath.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnvProjectAcceptsToolVersionsOnlyProject verifies that
// `gale env --project` succeeds in a directory that has only a
// .tool-versions file (no gale.toml), matching the behaviour of
// `gale list --project` and other read-only commands.
// Issue #80: resolveEnvScope used config.FindGaleConfig directly,
// missing the .tool-versions fallback in projectConfigPath.
func TestEnvProjectAcceptsToolVersionsOnlyProject(t *testing.T) {
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

	if err := envCmd.RunE(envCmd, nil); err != nil {
		t.Fatalf(
			"envCmd.RunE with --project in .tool-versions-only dir: %v",
			err,
		)
	}

	output := buf.String()
	// Must export PATH pointing at the project .gale/current/bin.
	wantDir := filepath.Join(proj, ".gale", "current", "bin")
	if !strings.Contains(output, wantDir) {
		t.Errorf(
			"expected PATH to include project .gale dir %q; got:\n%s",
			wantDir, output,
		)
	}
}

// TestEnvAutoResolvesToProjectForToolVersionsOnly verifies that
// `gale env` (auto mode, no scope flag) resolves to the project
// scope when only .tool-versions is present, rather than silently
// falling back to global scope.
// Issue #80: auto branch also called FindGaleConfig directly.
func TestEnvAutoResolvesToProjectForToolVersionsOnly(t *testing.T) {
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
	// Auto mode must resolve to the project .gale dir, not global.
	wantDir := filepath.Join(proj, ".gale", "current", "bin")
	if !strings.Contains(output, wantDir) {
		t.Errorf(
			"auto mode: expected PATH to include project .gale dir %q; got:\n%s",
			wantDir, output,
		)
	}
}
