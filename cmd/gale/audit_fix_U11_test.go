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
	configPath := filepath.Join(galeDir, "gale.toml")
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := finalizeInstall(
		galeDir, storeRoot, configPath, "otherbox",
		"hello", "1.0.0", "1.0.0-1", "abc123", "",
	)
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
// is missing (here: no store dir, so the lenient rebuild
// skips it).
func TestFinalizeInstallCurrentHostGenCheckStillEnforced(t *testing.T) {
	t.Setenv("GALE_HOST", "thishost")
	home := t.TempDir()
	t.Setenv("HOME", home)

	galeDir := filepath.Join(home, ".gale")
	storeRoot := filepath.Join(galeDir, "pkg")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(galeDir, "gale.toml")
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := finalizeInstall(
		galeDir, storeRoot, configPath, "thishost",
		"hello", "1.0.0", "1.0.0-1", "abc123", "",
	)
	if err == nil {
		t.Fatal("current-host install with no store dir must " +
			"still fail the active-generation check")
	}
}

// --- gh#73: pin/unpin must expose -g/-p scope flags ---

// TestPinUnpinRegisterScopeFlags pins gh#73: every mutating
// command exposes -g/--global and -p/--project; pin and unpin
// must too.
func TestPinUnpinRegisterScopeFlags(t *testing.T) {
	cases := []struct {
		cmdName string
		flag    string
		short   string
	}{
		{"pin", "global", "g"},
		{"pin", "project", "p"},
		{"unpin", "global", "g"},
		{"unpin", "project", "p"},
	}
	for _, tc := range cases {
		c := pinCmd
		if tc.cmdName == "unpin" {
			c = unpinCmd
		}
		f := c.Flags().Lookup(tc.flag)
		if f == nil {
			t.Errorf("%s: missing --%s flag", tc.cmdName, tc.flag)
			continue
		}
		if f.Shorthand != tc.short {
			t.Errorf("%s --%s: shorthand = %q, want %q",
				tc.cmdName, tc.flag, f.Shorthand, tc.short)
		}
	}
}

// TestPinAutoTargetsGlobalOutsideProject pins the gh#73
// behavior gap: from a non-project directory, pin must
// resolve to the global config (like install/add/remove do
// via resolveScope) instead of failing on a non-existent
// <cwd>/gale.toml.
func TestPinAutoTargetsGlobalOutsideProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	galeDir := filepath.Join(home, ".gale")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	globalConfig := filepath.Join(galeDir, "gale.toml")
	if err := os.WriteFile(globalConfig,
		[]byte("[packages]\njq = \"1.7\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })

	if err := pinCmd.RunE(pinCmd, []string{"jq"}); err != nil {
		t.Fatalf("pin from non-project dir must target the "+
			"global config: %v", err)
	}

	data, err := os.ReadFile(globalConfig)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.ParseGaleConfig(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Pinned["jq"] {
		t.Errorf("expected jq pinned in global config; got:\n%s",
			string(data))
	}
}

// TestUnpinAutoTargetsGlobalOutsideProject is the unpin half
// of gh#73.
func TestUnpinAutoTargetsGlobalOutsideProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	galeDir := filepath.Join(home, ".gale")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	globalConfig := filepath.Join(galeDir, "gale.toml")
	if err := os.WriteFile(globalConfig,
		[]byte("[packages]\njq = \"1.7\"\n\n[pinned]\njq = true\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })

	if err := unpinCmd.RunE(unpinCmd, []string{"jq"}); err != nil {
		t.Fatalf("unpin from non-project dir must target the "+
			"global config: %v", err)
	}

	data, err := os.ReadFile(globalConfig)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.ParseGaleConfig(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Pinned["jq"] {
		t.Errorf("expected jq unpinned in global config; got:\n%s",
			string(data))
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

// TestRepairDoctorUnderGlobalHomeNoNestedGaleDir pins the
// worst gh#96 symptom: `gale doctor --repair` run from inside
// ~/.gale treated the global config as a project config and
// CREATED the bogus ~/.gale/.gale directory on disk.
func TestRepairDoctorUnderGlobalHomeNoNestedGaleDir(t *testing.T) {
	galeDir := globalHomeFixture(t, "[packages]\n")

	var buf bytes.Buffer
	ctx := &doctorContext{
		galeDir:   galeDir,
		storeRoot: filepath.Join(galeDir, "pkg"),
		cwd:       galeDir,
		out:       output.NewWithOptions(&buf, output.Options{}),
	}
	if err := repairDoctor(ctx); err != nil {
		t.Fatalf("repairDoctor: %v", err)
	}

	nested := filepath.Join(galeDir, ".gale")
	if _, err := os.Stat(nested); err == nil {
		t.Errorf("repairDoctor created bogus %s", nested)
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
