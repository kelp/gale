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
// issue #50: checkFarm validated the GLOBAL farm against the
// merged global+project package set. A project-only package
// shipping a versioned dylib then produced permanent
// "missing farm entry" drift on the global farm — drift that
// `gale doctor --repair` (which rebuilds the global farm from
// global config only) can never fix.
//
// Setup: global config declares nothing; the project declares
// fakelib, whose dylib is correctly farmed in the PROJECT farm
// (<proj>/.gale/lib). The global farm is rightly empty. A
// scope-correct checkFarm must pass.
func TestCheckFarmScopesGlobalFarmToGlobalPackages(t *testing.T) {
	dylib := versionedDylibName(t)
	home := t.TempDir()
	galeDir := filepath.Join(home, ".gale")
	storeRoot := filepath.Join(galeDir, "pkg")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	target := fakelibStore(t, storeRoot, dylib)
	projectDir := projectWithFakelib(t, home)

	// Project farm is in sync: it has the symlink the
	// project generation's farm rebuild would create.
	projFarm := filepath.Join(projectDir, ".gale", "lib")
	if err := os.MkdirAll(projFarm, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(projFarm, dylib)); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	ctx := &doctorContext{
		galeDir:    galeDir,
		storeRoot:  storeRoot,
		cwd:        projectDir,
		globalPkgs: map[string]string{},
		projPkgs:   map[string]string{"fakelib": "1.0.0"},
		out:        output.NewWithOptions(&buf, output.Options{}),
	}

	if !checkFarm(ctx) {
		t.Fatalf("checkFarm must not flag the global farm for a "+
			"project-only package; output: %q", buf.String())
	}
	if strings.Contains(buf.String(), "missing farm entry") {
		t.Errorf("false drift reported: %q", buf.String())
	}
}

// TestCheckFarmDetectsProjectFarmDrift pins the other half of
// issue #50: the project farm (<proj>/.gale/lib) was never
// inspected, so real drift there was invisible. Here the
// project farm is missing fakelib's dylib entry while the
// global farm — wrongly consulted by the old merged check —
// happens to contain it. checkFarm must fail.
func TestCheckFarmDetectsProjectFarmDrift(t *testing.T) {
	dylib := versionedDylibName(t)
	home := t.TempDir()
	galeDir := filepath.Join(home, ".gale")
	storeRoot := filepath.Join(galeDir, "pkg")

	target := fakelibStore(t, storeRoot, dylib)
	projectDir := projectWithFakelib(t, home)

	// Global farm has the entry (would satisfy the old
	// merged check), but fakelib is NOT a global package.
	globalFarm := filepath.Join(galeDir, "lib")
	if err := os.MkdirAll(globalFarm, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(globalFarm, dylib)); err != nil {
		t.Fatal(err)
	}

	// Project farm exists but is empty — real drift.
	projFarm := filepath.Join(projectDir, ".gale", "lib")
	if err := os.MkdirAll(projFarm, 0o755); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	ctx := &doctorContext{
		galeDir:    galeDir,
		storeRoot:  storeRoot,
		cwd:        projectDir,
		globalPkgs: map[string]string{},
		projPkgs:   map[string]string{"fakelib": "1.0.0"},
		out:        output.NewWithOptions(&buf, output.Options{}),
	}

	if checkFarm(ctx) {
		t.Fatalf("checkFarm must detect drift in the project farm; "+
			"output: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "missing farm entry") {
		t.Errorf("expected missing-farm-entry drift for the project "+
			"farm; got: %q", buf.String())
	}
}
