package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func findCmd(name string) *cobra.Command {
	for _, c := range rootCmd.Commands() {
		if c.Name() == name {
			return c
		}
	}
	return nil
}

func findSub(parent, name string) *cobra.Command {
	p := findCmd(parent)
	if p == nil {
		return nil
	}
	for _, s := range p.Commands() {
		if s.Name() == name {
			return s
		}
	}
	return nil
}

// TestReadonlyCommandsHaveScopeFlags verifies every read-only
// command that should accept scope overrides exposes -g/--global
// and -p/--project.
func TestReadonlyCommandsHaveScopeFlags(t *testing.T) {
	cases := []string{
		"list", "info", "sbom", "outdated", "env", "which",
		"verify", "audit", "generations",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			c := findCmd(name)
			if c == nil {
				t.Fatalf("%s command not registered", name)
			}
			gFlag := c.Flags().Lookup("global")
			if gFlag == nil {
				t.Errorf("%s: --global flag not found", name)
			} else if gFlag.Shorthand != "g" {
				t.Errorf("%s: --global shorthand = %q, want %q",
					name, gFlag.Shorthand, "g")
			}
			pFlag := c.Flags().Lookup("project")
			if pFlag == nil {
				t.Errorf("%s: --project flag not found", name)
			} else if pFlag.Shorthand != "p" {
				t.Errorf("%s: --project shorthand = %q, want %q",
					name, pFlag.Shorthand, "p")
			}
		})
	}
}

// TestReadonlyInventoryCommandsHaveAllFlag verifies list and
// sbom expose -a/--all for cross-scope inventory output.
func TestReadonlyInventoryCommandsHaveAllFlag(t *testing.T) {
	cases := []string{"list", "sbom"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			c := findCmd(name)
			if c == nil {
				t.Fatalf("%s command not registered", name)
			}
			aFlag := c.Flags().Lookup("all")
			if aFlag == nil {
				t.Errorf("%s: --all flag not found", name)
			} else if aFlag.Shorthand != "a" {
				t.Errorf("%s: --all shorthand = %q, want %q",
					name, aFlag.Shorthand, "a")
			}
		})
	}
}

// TestGenerationsSubcommandsHaveScopeFlags verifies the
// generations subcommands (diff, rollback) define -g/-p.
func TestGenerationsSubcommandsHaveScopeFlags(t *testing.T) {
	subs := []string{"diff", "rollback"}
	for _, sub := range subs {
		t.Run(sub, func(t *testing.T) {
			s := findSub("generations", sub)
			if s == nil {
				t.Fatalf("generations %s not registered", sub)
			}
			if s.Flags().Lookup("global") == nil {
				t.Errorf("generations %s: --global flag not found", sub)
			}
			if s.Flags().Lookup("project") == nil {
				t.Errorf("generations %s: --project flag not found", sub)
			}
		})
	}
}

// TestListAllShowsBothScopes verifies `gale list --all` prints
// packages from both project and global gale.toml with clear
// section headers.
func TestListAllShowsBothScopes(t *testing.T) {
	home := t.TempDir()
	galeDir := filepath.Join(home, ".gale")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(galeDir, "gale.toml"),
		[]byte("[packages]\n  globalpkg = \"1.0\"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	projDir := filepath.Join(home, "proj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(projDir, "gale.toml"),
		[]byte("[packages]\n  projpkg = \"2.0\"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	os.Chdir(projDir)
	t.Cleanup(func() { os.Chdir(orig) })

	listScope = "all"
	listAll = true
	t.Cleanup(func() {
		listScope = "all"
		listAll = false
	})

	var buf bytes.Buffer
	if err := runList(&buf, &buf); err != nil {
		t.Fatalf("runList: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "projpkg@2.0") {
		t.Errorf("missing projpkg in --all output: %q", out)
	}
	if !strings.Contains(out, "globalpkg@1.0") {
		t.Errorf("missing globalpkg in --all output: %q", out)
	}
	if !strings.Contains(strings.ToLower(out), "project") {
		t.Errorf("--all output missing project header: %q", out)
	}
	if !strings.Contains(strings.ToLower(out), "global") {
		t.Errorf("--all output missing global header: %q", out)
	}
}

// TestListGlobalFromInsideProject verifies that `gale list -g`
// from inside a project directory shows only the global
// packages.
func TestListGlobalFromInsideProject(t *testing.T) {
	home := t.TempDir()
	galeDir := filepath.Join(home, ".gale")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(galeDir, "gale.toml"),
		[]byte("[packages]\n  globalpkg = \"1.0\"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	projDir := filepath.Join(home, "proj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(projDir, "gale.toml"),
		[]byte("[packages]\n  projpkg = \"2.0\"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	os.Chdir(projDir)
	t.Cleanup(func() { os.Chdir(orig) })

	listGlobal = true
	t.Cleanup(func() { listGlobal = false })

	var buf bytes.Buffer
	if err := runList(&buf, &buf); err != nil {
		t.Fatalf("runList: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "globalpkg@1.0") {
		t.Errorf("--global from project must show globalpkg: %q",
			out)
	}
	if strings.Contains(out, "projpkg") {
		t.Errorf("--global must NOT show projpkg: %q", out)
	}
}

// TestInfoGlobalOverridesProjectShadow verifies that with
// --global, info reads the global gale.toml even when invoked
// inside a project that shadows the package.
func TestInfoGlobalOverridesProjectShadow(t *testing.T) {
	home := t.TempDir()
	galeDir := filepath.Join(home, ".gale")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(galeDir, "gale.toml"),
		[]byte("[packages]\n  jq = \"1.7\"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	projDir := filepath.Join(home, "proj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(projDir, "gale.toml"),
		[]byte("[packages]\n  jq = \"1.8\"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	os.Chdir(projDir)
	t.Cleanup(func() { os.Chdir(orig) })

	infoGlobal = true
	t.Cleanup(func() { infoGlobal = false })

	var buf bytes.Buffer
	if err := runInfo(&buf, "jq"); err != nil {
		t.Fatalf("runInfo: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "1.7") {
		t.Errorf("info --global must show 1.7, got: %q", out)
	}
	if !strings.Contains(strings.ToLower(out), "global") {
		t.Errorf("info --global must report global scope: %q",
			out)
	}
}

// TestEnvGlobalFromInsideProjectExportsGlobalVars verifies
// `gale env -g` from inside a project exports the global
// gale.toml's [vars] (not the project's).
func TestEnvGlobalFromInsideProjectExportsGlobalVars(t *testing.T) {
	home := t.TempDir()
	galeDir := filepath.Join(home, ".gale")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(galeDir, "gale.toml"),
		[]byte("[packages]\n\n[vars]\nGLOBAL_VAR = \"yes\"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	projDir := filepath.Join(home, "proj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(projDir, "gale.toml"),
		[]byte("[packages]\n\n[vars]\nPROJECT_VAR = \"yes\"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	os.Chdir(projDir)
	t.Cleanup(func() { os.Chdir(orig) })

	envGlobal = true
	envVarsOnly = true
	t.Cleanup(func() {
		envGlobal = false
		envVarsOnly = false
	})

	var buf bytes.Buffer
	envCmd.SetOut(&buf)
	if err := envCmd.RunE(envCmd, nil); err != nil {
		t.Fatalf("envCmd.RunE: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "GLOBAL_VAR") {
		t.Errorf("env -g must export GLOBAL_VAR, got: %q", out)
	}
	if strings.Contains(out, "PROJECT_VAR") {
		t.Errorf("env -g must NOT export PROJECT_VAR, got: %q",
			out)
	}
}

// TestScopeFlagsMutuallyExclusive verifies that read-only
// commands reject --global and --project together.
func TestScopeFlagsMutuallyExclusive(t *testing.T) {
	if err := validateScopeFlags(true, true); err == nil {
		t.Error("expected error when both -g and -p set")
	}
	if err := validateScopeFlags(true, false); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if err := validateScopeFlags(false, true); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if err := validateScopeFlags(false, false); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestResolveReadOnlyConfigPathProjectMissingErrors verifies
// that requesting --project when no project gale.toml exists
// produces a clear error.
func TestResolveReadOnlyConfigPathProjectMissingErrors(t *testing.T) {
	tmp := t.TempDir()
	orig, _ := os.Getwd()
	os.Chdir(tmp)
	t.Cleanup(func() { os.Chdir(orig) })

	_, err := resolveReadOnlyConfigPath(false, true)
	if err == nil {
		t.Error("expected error when --project outside any project")
	}
}

// TestResolveReadOnlyConfigPathGlobalForced verifies that
// --global returns the global gale.toml path even from inside
// a project.
func TestResolveReadOnlyConfigPathGlobalForced(t *testing.T) {
	home := t.TempDir()
	galeDir := filepath.Join(home, ".gale")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	projDir := filepath.Join(home, "proj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(projDir, "gale.toml"),
		[]byte("[packages]\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	os.Chdir(projDir)
	t.Cleanup(func() { os.Chdir(orig) })

	got, err := resolveReadOnlyConfigPath(true, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(galeDir, "gale.toml")
	if got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}
