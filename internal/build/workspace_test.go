package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestMakeBuildWorkspaceEmptyParentFallsBackToTemp(t *testing.T) {
	// TmpDir() returns "" when the home dir is unavailable;
	// os.MkdirTemp treated "" as the system temp dir, and
	// makeBuildWorkspace must preserve that fallback rather
	// than creating a relative dir in the CWD.
	ws, err := makeBuildWorkspace("")
	if err != nil {
		t.Fatalf("makeBuildWorkspace: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(ws) })
	if !filepath.IsAbs(ws) {
		t.Errorf("workspace %q is not absolute; empty parent"+
			" must fall back to the system temp dir", ws)
	}
}
