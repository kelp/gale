package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/output"
)

// --- gh#72: install --host <other-host> is declaration-only ---

// TestFinalizeInstallForeignHostDeclarationOnly pins gh#72:
// `gale install --host otherbox` writes the package to
// [hosts.otherbox.packages], but the generation is rebuilt
// from the CURRENT host's effective set — the package is
// correctly absent from it. The post-rebuild presence check
// must be skipped for a foreign host instead of failing with
// a bogus "store dir removed mid-install" error after config,
// lock, and store were already mutated.
func TestFinalizeInstallForeignHostDeclarationOnly(t *testing.T) {
	t.Setenv("GALE_HOST", "thishost")
	home := t.TempDir()
	t.Setenv("HOME", home)

	galeDir := filepath.Join(home, ".gale")
	storeRoot := filepath.Join(galeDir, "pkg")
	if err := os.MkdirAll(filepath.Join(
		storeRoot, "hello", "1.0.0-1", "bin",
	), 0o755); err != nil {
		t.Fatal(err)
	}
	// The lock write resolves the closure from provenance, so a
	// package with no record cannot be locked at all and the install
	// would fail before reaching the check this test is about.
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
		Host:      "otherbox",
	}
	err := ctx.FinalizeInstall("hello", "1.0.0", "1.0.0-1")
	if err != nil {
		t.Fatalf("foreign-host install must not fail the "+
			"active-generation check (declaration-only): %v", err)
	}

	// The declaration must have landed in the foreign host's
	// overlay.
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.ParseGaleConfig(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hosts["otherbox"].Packages["hello"] != "1.0.0" {
		t.Errorf("expected hello in [hosts.otherbox.packages]; "+
			"config:\n%s", string(data))
	}
}

// TestFinalizeInstallCurrentHostGenCheckStillEnforced guards
// the gh#72 fix from over-correcting: when --host targets the
// CURRENT machine, the package belongs in the active
// generation and the presence check must still fire when it
// is missing (here: the config pin resolves to a version that is
// not on disk, so the lenient rebuild skips it and CurrentVersions
// never reports it). The installed revision exists and carries
// provenance, because the lock write precedes the check and needs a
// closure to describe; an absent store dir would fail the install
// earlier, for another reason, and stop testing this one.
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

// TestCheckProjectConfigUnderGlobalHome pins the gh#96
// double-report: from inside ~/.gale, checkProjectConfig must
// not report the global config as a project config or copy
// the global package set into projPkgs.
func TestCheckProjectConfigUnderGlobalHome(t *testing.T) {
	galeDir := globalHomeFixture(t,
		"[packages]\njq = \"1.7\"\nrg = \"14.0\"\n")

	var buf bytes.Buffer
	ctx := &doctorContext{
		galeDir:    galeDir,
		storeRoot:  filepath.Join(galeDir, "pkg"),
		cwd:        galeDir,
		globalPkgs: map[string]string{},
		projPkgs:   map[string]string{},
		out:        output.NewWithOptions(&buf, output.Options{}),
	}
	if !checkProjectConfig(ctx) {
		t.Fatalf("checkProjectConfig failed: %s", buf.String())
	}
	if len(ctx.projPkgs) != 0 {
		t.Errorf("global config double-reported as project: "+
			"projPkgs = %v", ctx.projPkgs)
	}
	if strings.Contains(buf.String(), "Project config") {
		t.Errorf("global config reported as project config: %q",
			buf.String())
	}
}

// TestCheckHostOverridesUnderGlobalHomeNoDoubleCount pins the
// gh#96 double-count: from inside ~/.gale, the global
// config's overrides were appended a second time as
// "project" overrides.
func TestCheckHostOverridesUnderGlobalHomeNoDoubleCount(t *testing.T) {
	t.Setenv("GALE_HOST", "testhost")
	galeDir := globalHomeFixture(t,
		"[packages]\njq = \"1.7\"\n\n"+
			"[hosts.testhost.packages]\njq = \"1.8\"\n")

	var buf bytes.Buffer
	ctx := &doctorContext{
		galeDir:    galeDir,
		storeRoot:  filepath.Join(galeDir, "pkg"),
		cwd:        galeDir,
		globalPkgs: map[string]string{},
		projPkgs:   map[string]string{},
		out:        output.NewWithOptions(&buf, output.Options{}),
	}
	checkHostOverrides(ctx)
	if !strings.Contains(buf.String(), "shadows 1 shared") {
		t.Errorf("expected exactly 1 shadow reported (not "+
			"double-counted); got: %q", buf.String())
	}
}
