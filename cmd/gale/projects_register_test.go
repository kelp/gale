package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/kelp/gale/internal/projects"
)

// newTestProject creates a project dir with an empty-package
// gale.toml and returns its symlink-resolved path (t.TempDir
// can sit behind symlinks, e.g. macOS /var → /private/var).
func newTestProject(t *testing.T) string {
	t.Helper()
	proj := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(proj, "gale.toml"),
		[]byte("[packages]\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(proj)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// registryContains reports whether the machine-local project
// registry under HOME lists proj.
func registryContains(t *testing.T, home, proj string) bool {
	t.Helper()
	list, err := projects.List(filepath.Join(home, ".gale"))
	if err != nil {
		t.Fatalf("listing registry: %v", err)
	}
	for _, p := range list {
		if p == proj {
			return true
		}
	}
	return false
}

// TestNewCmdContextRegistersProject verifies that resolving a
// project-scoped context records the project in the registry,
// so gc run from anywhere can retain its generation (gh#115).
func TestNewCmdContextRegistersProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	proj := newTestProject(t)
	t.Chdir(proj)

	if _, err := newCmdContext("", false, false); err != nil {
		t.Fatalf("newCmdContext: %v", err)
	}

	if !registryContains(t, home, proj) {
		t.Errorf("project %s not registered by newCmdContext",
			proj)
	}
}

// TestNewCmdContextSkipsGlobalScope verifies a global-scope
// context (no project anywhere) does not register ~/.gale as
// a project.
func TestNewCmdContextSkipsGlobalScope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir()) // neutral cwd, no project

	if _, err := newCmdContext("", false, false); err != nil {
		t.Fatalf("newCmdContext: %v", err)
	}

	list, err := projects.List(filepath.Join(home, ".gale"))
	if err != nil {
		t.Fatalf("listing registry: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("global scope must not register anything, "+
			"got %v", list)
	}
}

// TestEnvCommandRegistersProject verifies the direnv
// activation path (`use gale` runs `gale env`) registers the
// project (gh#115).
func TestEnvCommandRegistersProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	proj := newTestProject(t)
	t.Chdir(proj)

	envCmd.SetOut(io.Discard)
	t.Cleanup(func() { envCmd.SetOut(nil) })
	if err := envCmd.RunE(envCmd, nil); err != nil {
		t.Fatalf("gale env: %v", err)
	}

	if !registryContains(t, home, proj) {
		t.Errorf("project %s not registered by gale env", proj)
	}
}

// TestSyncProjectDirRegistersProject verifies sync with an
// explicit projectDir (the shell/run auto-sync path, which
// bypasses cwd detection) registers that project (gh#115).
func TestSyncProjectDirRegistersProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GALE_OFFLINE", "1")
	proj := newTestProject(t)
	t.Chdir(t.TempDir()) // cwd is NOT the project

	if err := runSync("", false, false, false, proj); err != nil {
		t.Fatalf("runSync: %v", err)
	}

	if !registryContains(t, home, proj) {
		t.Errorf("project %s not registered by sync", proj)
	}
}

// TestRegisterProjectSkipsDryRun verifies dry-run commands do
// not mutate the registry.
func TestRegisterProjectSkipsDryRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	proj := newTestProject(t)
	t.Chdir(proj)

	dryRun = true
	t.Cleanup(func() { dryRun = false })
	if _, err := newCmdContext("", false, false); err != nil {
		t.Fatalf("newCmdContext: %v", err)
	}

	if registryContains(t, home, proj) {
		t.Errorf("dry-run must not register projects")
	}
}
