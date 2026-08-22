package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestFinalizeInstallCurrentHostGenCheckStillEnforced(t *testing.T) {
	t.Setenv("GALE_HOST", "thishost")
	home := t.TempDir()
	t.Setenv("HOME", home)

	galeDir := filepath.Join(home, ".gale")
	storeRoot := filepath.Join(galeDir, "pkg")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeProvenance(t, storeRoot, "hello", "1.0.0-1")
	configPath := filepath.Join(galeDir, "gale.toml")
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := &cmdContext{
		GaleDir:   galeDir,
		StoreRoot: storeRoot,
		GalePath:  configPath,
		Host:      "thishost",
	}
	err := ctx.FinalizeInstall("hello", "9.9.9", "1.0.0-1")
	if err == nil {
		t.Fatal("current-host install with no store dir must " +
			"still fail the active-generation check")
	}
}

// --- gh#96: cwd under ~/.gale must not invent <~/.gale>/.gale ---

// globalHomeFixture creates HOME with a global gale.toml and
// chdirs into ~/.gale, the cwd that makes FindGaleConfig
// resolve to the GLOBAL config. Returns the global gale dir.
func globalHomeFixture(t *testing.T, configTOML string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	galeDir := filepath.Join(home, ".gale")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(galeDir, "gale.toml"),
		[]byte(configTOML), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	orig, _ := os.Getwd()
	if err := os.Chdir(galeDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })
	return galeDir
}

// TestResolveGaleDirUnderGlobalHome pins gh#96 for env.go:
// from inside ~/.gale, resolveGaleDir must return ~/.gale,
// not the bogus ~/.gale/.gale.
func TestResolveGaleDirUnderGlobalHome(t *testing.T) {
	galeDir := globalHomeFixture(t, "[packages]\n")

	got, err := resolveGaleDir()
	if err != nil {
		t.Fatal(err)
	}
	if !sameDir(got, galeDir) {
		t.Errorf("resolveGaleDir() = %q, want %q", got, galeDir)
	}
}

// TestResolveEnvScopeAutoUnderGlobalHome pins gh#96 for
// resolveEnvScope's auto path.
func TestResolveEnvScopeAutoUnderGlobalHome(t *testing.T) {
	galeDir := globalHomeFixture(t, "[packages]\n")

	got, _, err := resolveEnvScope(false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !sameDir(got, galeDir) {
		t.Errorf("resolveEnvScope(auto) galeDir = %q, want %q",
			got, galeDir)
	}
}

// TestResolveGenerationsGaleDirUnderGlobalHome pins gh#96 for
// generations.go.
func TestResolveGenerationsGaleDirUnderGlobalHome(t *testing.T) {
	galeDir := globalHomeFixture(t, "[packages]\n")

	got, err := resolveGenerationsGaleDir(false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !sameDir(got, galeDir) {
		t.Errorf("resolveGenerationsGaleDir(auto) = %q, want %q",
			got, galeDir)
	}
}

// TestDoctorUnderGlobalHomeNoNestedGaleDir pins the worst
// gh#96 symptom: doctor run from inside ~/.gale treated the
// global config as a project config and invented the bogus
// ~/.gale/.gale directory.
func TestDoctorUnderGlobalHomeNoNestedGaleDir(t *testing.T) {
	galeDir := globalHomeFixture(t, "[packages]\n")

	var buf bytes.Buffer
	_ = runDoctor(&doctorIO{
		galeDir: galeDir,
		cwd:     galeDir,
		stdout:  &buf,
		stderr:  &buf,
	})

	nested := filepath.Join(galeDir, ".gale")
	if _, err := os.Stat(nested); err == nil {
		t.Errorf("doctor created bogus %s", nested)
	}
}
