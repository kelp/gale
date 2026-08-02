package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/output"
)

// versionedDylibName returns a farm-eligible versioned
// shared-library basename for the current OS, matching the
// patterns in internal/farm.
func versionedDylibName(t *testing.T) string {
	t.Helper()
	switch runtime.GOOS {
	case "darwin":
		return "libfake.1.2.3.dylib"
	case "linux":
		return "libfake.so.1.2.3"
	default:
		t.Skip("farm only supports darwin and linux")
		return ""
	}
}

// fakelibStore creates storeRoot/fakelib/1.0.0-1/lib/<dylib>
// and returns the path to the dylib inside the store.
func fakelibStore(t *testing.T, storeRoot, dylib string) string {
	t.Helper()
	libDir := filepath.Join(storeRoot, "fakelib", "1.0.0-1", "lib")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(libDir, dylib)
	if err := os.WriteFile(target, []byte("not really elf"), 0o644); err != nil {
		t.Fatal(err)
	}
	return target
}

// projectWithFakelib creates home/project with a gale.toml
// declaring fakelib, and returns the project dir.
func projectWithFakelib(t *testing.T, home string) string {
	t.Helper()
	projectDir := filepath.Join(home, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "[packages]\nfakelib = \"1.0.0\"\n"
	if err := os.WriteFile(filepath.Join(projectDir, "gale.toml"),
		[]byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return projectDir
}

// TestCheckFarmScopesGlobalFarmToGlobalPackages reproduces
// issue #50: checkFarm validated the farm against the MERGED
// global+project package set. A project-only package shipping a
// versioned dylib then produced permanent "missing farm entry"
// drift on the global scope — drift that `gale doctor --repair`
// (which rebuilds from global config only) can never fix.
//
// Design revision 6 retired the per-project farm, so there is now
// one shared farm and both scopes read it. That makes the scoping
// rule MORE load-bearing, not less: the two checks can no longer
// be told apart by which directory they read, only by which
// package set they demand. The fixture leaves the shared farm
// empty and declares fakelib in the project alone; the global
// check must still pass, because a merged one would not.
func TestCheckFarmScopesGlobalFarmToGlobalPackages(t *testing.T) {
	dylib := versionedDylibName(t)
	home := t.TempDir()
	galeDir := filepath.Join(home, ".gale")
	storeRoot := filepath.Join(galeDir, "pkg")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	fakelibStore(t, storeRoot, dylib)
	projectDir := projectWithFakelib(t, home)

	var buf bytes.Buffer
	ctx := &doctorContext{
		galeDir:    galeDir,
		storeRoot:  storeRoot,
		cwd:        projectDir,
		globalPkgs: map[string]string{},
		projPkgs:   map[string]string{"fakelib": "1.0.0"},
		out:        output.NewWithOptions(&buf, output.Options{}),
	}

	if !checkFarmScope(ctx, ctx.globalPkgs) {
		t.Fatalf("the global scope must not be flagged for a "+
			"project-only package; output: %q", buf.String())
	}
	if strings.Contains(buf.String(), "missing farm entry") {
		t.Errorf("false drift reported: %q", buf.String())
	}
}

// TestCheckFarmSkipsProjectScopeUnderGaleHome pins the
// false-positive found in review: when cwd is inside the
// global gale home, config.FindGaleConfig resolves to the
// GLOBAL gale.toml. Deriving the project gale dir from it
// yields the bogus <galeDir>/.gale, and checkFarm then
// reports drift that `gale doctor --repair` can never fix.
// The project-scope check must be skipped when the found
// config is the global one — the global farm was already
// checked.
func TestCheckFarmSkipsProjectScopeUnderGaleHome(t *testing.T) {
	dylib := versionedDylibName(t)
	home := t.TempDir()
	galeDir := filepath.Join(home, ".gale")
	storeRoot := filepath.Join(galeDir, "pkg")

	target := fakelibStore(t, storeRoot, dylib)
	cfg := "[packages]\nfakelib = \"1.0.0\"\n"
	if err := os.WriteFile(filepath.Join(galeDir, "gale.toml"),
		[]byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	// Global farm is in sync.
	globalFarm := filepath.Join(galeDir, "lib")
	if err := os.MkdirAll(globalFarm, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(globalFarm, dylib)); err != nil {
		t.Fatal(err)
	}

	// cwd is INSIDE the global gale home, so FindGaleConfig
	// resolves to the global gale.toml. projPkgs mirrors what
	// checkProjectConfig sets in that situation (the global
	// packages, conflated as "project" packages).
	var buf bytes.Buffer
	ctx := &doctorContext{
		galeDir:    galeDir,
		storeRoot:  storeRoot,
		cwd:        filepath.Join(galeDir, "pkg"),
		globalPkgs: map[string]string{"fakelib": "1.0.0"},
		projPkgs:   map[string]string{"fakelib": "1.0.0"},
		out:        output.NewWithOptions(&buf, output.Options{}),
	}

	if !checkFarm(ctx) {
		t.Fatalf("checkFarm must not report drift when cwd is "+
			"under the global gale home; output: %q", buf.String())
	}
	bogus := filepath.Join(galeDir, ".gale")
	if resolved, err := filepath.EvalSymlinks(galeDir); err == nil {
		bogus = filepath.Join(resolved, ".gale")
	}
	if strings.Contains(buf.String(), bogus) ||
		strings.Contains(buf.String(), filepath.Join(galeDir, ".gale")) {
		t.Errorf("bogus farm path %q was checked; output: %q",
			bogus, buf.String())
	}
}

// TestCheckFarmDetectsProjectFarmDrift pins the other half of
// issue #50: the project farm (<proj>/.gale/lib) was never
// inspected, so real drift there was invisible. Here the
// project farm is missing fakelib's dylib entry while the
// global farm — wrongly consulted by the old merged check —
// TestCheckFarmDetectsProjectDrift is the other arm of the same
// fixture: the project scope DOES demand fakelib, so the empty
// shared farm is real drift and must be reported.
//
// Before design revision 6 this read <proj>/.gale/lib, a directory
// nothing resolves through, so a project doctor run reported a
// clean farm however broken the real one was.
func TestCheckFarmDetectsProjectDrift(t *testing.T) {
	dylib := versionedDylibName(t)
	home := t.TempDir()
	galeDir := filepath.Join(home, ".gale")
	storeRoot := filepath.Join(galeDir, "pkg")

	fakelibStore(t, storeRoot, dylib)
	projectDir := projectWithFakelib(t, home)

	var buf bytes.Buffer
	ctx := &doctorContext{
		galeDir:    galeDir,
		storeRoot:  storeRoot,
		cwd:        projectDir,
		globalPkgs: map[string]string{},
		projPkgs:   map[string]string{"fakelib": "1.0.0"},
		out:        output.NewWithOptions(&buf, output.Options{}),
	}

	if checkFarmScope(ctx, ctx.projPkgs) {
		t.Fatalf("checkFarm must detect the project's missing entry "+
			"in the shared farm; output: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "missing farm entry") {
		t.Errorf("expected missing-farm-entry drift for the project "+
			"scope; got: %q", buf.String())
	}
}
