package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/recipe"
)

// --- gale-recipes#79: builds must be byte-reproducible ---
//
// The workspace path is baked into Mach-O install names at link
// time. FixupBinaries rewrites the strings to @rpath, but
// install_name_tool preserves each load command's original size,
// so two builds whose workspace paths differ in LENGTH ship
// different load-command layouts. os.MkdirTemp's random suffix
// varies from 1 to 10 digits; the workspace suffix must not.

func TestMakeBuildWorkspaceFixedLengthSuffix(t *testing.T) {
	parent := t.TempDir()
	seen := make(map[string]bool)
	var wantLen int
	for i := range 32 {
		ws, err := makeBuildWorkspace(parent)
		if err != nil {
			t.Fatalf("makeBuildWorkspace: %v", err)
		}
		base := filepath.Base(ws)
		if !strings.HasPrefix(base, "gale-build-") {
			t.Fatalf("workspace %q lacks gale-build- prefix", base)
		}
		if i == 0 {
			wantLen = len(base)
		}
		if len(base) != wantLen {
			t.Errorf("workspace name length varies: %q (%d) vs"+
				" first (%d)", base, len(base), wantLen)
		}
		if seen[base] {
			t.Errorf("duplicate workspace name %q", base)
		}
		seen[base] = true
	}
}

// TestMakeBuildWorkspaceFallsBackToTemp keeps the assertion that
// used to live in TestMakeBuildWorkspaceEmptyParentFallsBackToTemp:
// when ~/.gale/tmp is unavailable, the workspace still lands in an
// absolute system-temp path rather than a relative directory in the
// CWD.
//
// It now passes for a different reason. The fallback moved up into
// TmpDir() (gh#235), which resolves and verifies os.TempDir()
// itself, so makeBuildWorkspace no longer carries a parent == ""
// special case — its parent argument is always a real directory.
// The behavior under test is unchanged; only its owner is.
func TestMakeBuildWorkspaceFallsBackToTemp(t *testing.T) {
	breakGaleDir(t)
	captureBuildOutput(t)

	parent, err := TmpDir()
	if err != nil {
		t.Fatalf("TmpDir: %v", err)
	}

	ws, err := makeBuildWorkspace(parent)
	if err != nil {
		t.Fatalf("makeBuildWorkspace: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(ws) })

	if !filepath.IsAbs(ws) {
		t.Errorf("workspace %q is not absolute; the fallback must"+
			" name the system temp dir, not the CWD", ws)
	}
	assertSamePath(t, filepath.Dir(ws), os.TempDir())
}

// TestBuildSurvivesUnusableGaleDir pins the contract at the
// exported entry points, where the fallback actually matters: an
// unusable ~/.gale must not stop a build before it starts. Both
// recipes point at an unroutable source, so the build is expected
// to fail — just not at workspace creation.
func TestBuildSurvivesUnusableGaleDir(t *testing.T) {
	breakGaleDir(t)
	captureBuildOutput(t)

	r := &recipe.Recipe{
		Package: recipe.Package{Name: "testpkg", Version: "1.0"},
		Source:  recipe.Source{URL: "http://127.0.0.1:1/x.tar.gz"},
		Build:   recipe.Build{Steps: []string{"true"}},
	}

	_, err := Build(r, t.TempDir(), false, nil)
	if err == nil {
		t.Fatal("Build unexpectedly succeeded against an unroutable source")
	}
	if strings.Contains(err.Error(), "build temp dir") {
		t.Errorf("Build failed on scratch allocation (%v); an "+
			"unusable ~/.gale must fall back, not abort", err)
	}
}

// TestBuildErrorsWhenNoScratchDirAvailable is the other side: with
// no usable location anywhere, the failure is surfaced rather than
// written somewhere arbitrary.
func TestBuildErrorsWhenNoScratchDirAvailable(t *testing.T) {
	// Both temp dirs are allocated before TMPDIR is broken:
	// t.TempDir() resolves through os.TempDir() too.
	outputDir, sourceDir := t.TempDir(), t.TempDir()
	breakGaleDir(t)
	breakSystemTemp(t)

	r := &recipe.Recipe{
		Package: recipe.Package{Name: "testpkg", Version: "1.0"},
		Source:  recipe.Source{URL: "http://127.0.0.1:1/x.tar.gz"},
		Build:   recipe.Build{Steps: []string{"true"}},
	}

	_, err := Build(r, outputDir, false, nil)
	if err == nil {
		t.Fatal("Build returned nil error with no usable scratch dir")
	}
	if !strings.Contains(err.Error(), "build temp dir") {
		t.Errorf("Build error %q does not name the scratch dir as "+
			"the cause", err)
	}

	if _, err := BuildLocal(r, sourceDir, outputDir, false, nil); err == nil {
		t.Fatal("BuildLocal returned nil error with no usable scratch dir")
	}
}
