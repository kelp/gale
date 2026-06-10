package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/depsmeta"
	"github.com/kelp/gale/internal/output"
)

// TestCheckFarmDetectsMissingDepDylib pins the populate/check
// asymmetry flagged in PR #99 review: generation.Build and
// rollback populate the farm from FarmStoreDirs (config
// packages plus the transitive runtime-dep closure recorded
// in .gale-deps.toml), but checkFarmScope validated with
// ActiveStoreDirs (config packages only). A farm missing a
// dep's dylib — exactly the breakage FarmStoreDirs exists to
// prevent — passed `gale doctor` even though dependents'
// rpaths could not resolve through it.
//
// Setup: global config declares "app", whose store dir
// records runtime dep "fakelib" in .gale-deps.toml. fakelib's
// versioned dylib is present in the store but MISSING from
// the farm. Doctor's farm check must report drift.
func TestCheckFarmDetectsMissingDepDylib(t *testing.T) {
	dylib := versionedDylibName(t)
	home := t.TempDir()
	galeDir := filepath.Join(home, ".gale")
	storeRoot := filepath.Join(galeDir, "pkg")

	// Dep package: dylib in the store, recorded nowhere in
	// config — only in app's .gale-deps.toml.
	fakelibStore(t, storeRoot, dylib)

	// Config package: no dylibs of its own, runtime dep on
	// fakelib recorded in its deps metadata.
	appDir := filepath.Join(storeRoot, "app", "1.0.0-1")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	md := depsmeta.Metadata{Deps: []depsmeta.ResolvedDep{
		{Name: "fakelib", Version: "1.0.0", Revision: 1},
	}}
	if err := depsmeta.Write(appDir, md); err != nil {
		t.Fatal(err)
	}

	// Farm exists but is empty: fakelib's dylib is missing —
	// the drift generation.Build's farm rebuild would fix.
	if err := os.MkdirAll(filepath.Join(galeDir, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	ctx := &doctorContext{
		galeDir:    galeDir,
		storeRoot:  storeRoot,
		cwd:        home,
		globalPkgs: map[string]string{"app": "1.0.0"},
		projPkgs:   map[string]string{},
		out:        output.NewWithOptions(&buf, output.Options{}),
	}

	if checkFarm(ctx) {
		t.Fatalf("checkFarm must detect a dep dylib missing from "+
			"the farm; output: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "missing farm entry") {
		t.Errorf("expected missing-farm-entry drift for the dep "+
			"dylib; got: %q", buf.String())
	}
}
