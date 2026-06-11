package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// chdirTo changes the working directory to dir for the duration
// of the test and restores it on cleanup.
func chdirTo(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

// TestEnvSurfacesMalformedProjectConfig verifies that a
// malformed project gale.toml causes `gale env` to return an
// error rather than silently exiting 0. Matches the behaviour
// of peer read-only commands (list, info, sbom).
// Audit finding: exit-codes/0001.
func TestEnvSurfacesMalformedProjectConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(proj, "gale.toml"),
		[]byte("this is = not [ valid toml\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	chdirTo(t, proj)

	var buf bytes.Buffer
	envCmd.SetOut(&buf)
	envVarsOnly = false
	t.Cleanup(func() { envVarsOnly = false })

	if err := envCmd.RunE(envCmd, nil); err == nil {
		t.Fatalf("expected error for malformed gale.toml, "+
			"got nil; stdout=%q", buf.String())
	}
}

// TestEnvVarsOnlySurfacesMalformedConfig verifies that
// --vars-only also surfaces parse errors instead of emitting
// empty stdout with exit 0. Audit finding: empty-state/0003.
func TestEnvVarsOnlySurfacesMalformedConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(proj, "gale.toml"),
		[]byte("[packages\njq = not_quoted\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	chdirTo(t, proj)

	var buf bytes.Buffer
	envCmd.SetOut(&buf)
	envVarsOnly = true
	t.Cleanup(func() { envVarsOnly = false })

	if err := envCmd.RunE(envCmd, nil); err == nil {
		t.Fatalf("expected error for malformed gale.toml in "+
			"vars-only mode, got nil; stdout=%q",
			buf.String())
	}
}

// TestEnvExportsGlobalVarsWhenNoProjectConfig verifies that
// when env resolves to the global scope (no project gale.toml
// in the walk-up chain), [vars] from ~/.gale/gale.toml are
// exported. Audit finding: scope-behaviour/0002.
func TestEnvExportsGlobalVarsWhenNoProjectConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	galeDir := filepath.Join(home, ".gale")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(galeDir, "gale.toml"),
		[]byte("[packages]\n\n[vars]\n"+
			"GLOBAL_VAR = \"world\"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	// Run from a directory with no project gale.toml in any
	// ancestor. The global gale.toml lives at home/.gale/
	// which is NOT an ancestor of home/elsewhere, so the
	// walk-up cannot find it.
	cwd := filepath.Join(home, "elsewhere")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	chdirTo(t, cwd)

	var buf bytes.Buffer
	envCmd.SetOut(&buf)
	envVarsOnly = true
	t.Cleanup(func() { envVarsOnly = false })

	if err := envCmd.RunE(envCmd, nil); err != nil {
		t.Fatalf("envCmd.RunE: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "export GLOBAL_VAR='world'") {
		t.Errorf("expected global var to be exported, got:\n%s",
			output)
	}
}

// TestEnvNoConfigAtAllIsNotAnError verifies that running env
// in a directory chain with no gale.toml (project or global)
// is not an error — it just prints PATH and no vars.
func TestEnvNoConfigAtAllIsNotAnError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cwd := filepath.Join(home, "empty")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	chdirTo(t, cwd)

	var buf bytes.Buffer
	envCmd.SetOut(&buf)
	envVarsOnly = false
	t.Cleanup(func() { envVarsOnly = false })

	if err := envCmd.RunE(envCmd, nil); err != nil {
		t.Fatalf("envCmd.RunE: %v", err)
	}
}

func TestEnvVarsUseShellQuoting(t *testing.T) {
	// Go's %q produces Go-syntax escape sequences
	// (e.g. \t for tab) which POSIX sh doesn't
	// understand. Vars must be single-quoted for
	// shell safety.
	t.Setenv("HOME", t.TempDir()) // isolate ~/.gale (project registry)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "gale.toml")
	if err := os.WriteFile(configPath, []byte(
		"[packages]\n\n[vars]\nFOO = \"hello world\"\n",
	),
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
			output,
		)
	}
}

func TestEnvVarsEscapeEmbeddedSingleQuotes(t *testing.T) {
	// Values with embedded single quotes must be escaped
	// using the '\'' idiom.
	t.Setenv("HOME", t.TempDir()) // isolate ~/.gale (project registry)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "gale.toml")
	if err := os.WriteFile(configPath, []byte(
		"[packages]\n\n[vars]\nMSG = \"it's fine\"\n",
	),
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
