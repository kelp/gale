package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kelp/gale/internal/generation"
)

// auditU1StorePkg creates <storeRoot>/<name>/<version>/bin/<name>
// with content so the dir counts as a real install.
func auditU1StorePkg(t *testing.T, storeRoot, name, version string) {
	t.Helper()
	binDir := filepath.Join(storeRoot, name, version, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("setup %s/%s: %v", name, version, err)
	}
	if err := os.WriteFile(
		filepath.Join(binDir, name), []byte("fake"), 0o755,
	); err != nil {
		t.Fatalf("setup binary %s/%s: %v", name, version, err)
	}
}

func TestGenerationDriftedFalseWhenConfigUnchanged(t *testing.T) {
	tmp := t.TempDir()
	galeDir := filepath.Join(tmp, ".gale")
	storeRoot := filepath.Join(tmp, "pkg")

	auditU1StorePkg(t, storeRoot, "jq", "1.8.1-4")

	pkgs := map[string]string{"jq": "1.8.1"}
	if err := generation.Build(pkgs, galeDir, storeRoot); err != nil {
		t.Fatalf("Build: %v", err)
	}

	if generationDrifted(galeDir, storeRoot, pkgs, nil) {
		t.Error("generationDrifted = true for an unchanged config " +
			"(gh#49: every no-op sync rebuilds the generation)")
	}
}

// TestGenerationDriftedTrueWhenPackageRemoved guards the reason
// the drift check exists at all: dropping a package from the
// config must still report drift so the rebuild removes its
// symlinks from PATH.
func TestGenerationDriftedTrueWhenPackageRemoved(t *testing.T) {
	tmp := t.TempDir()
	galeDir := filepath.Join(tmp, ".gale")
	storeRoot := filepath.Join(tmp, "pkg")

	auditU1StorePkg(t, storeRoot, "jq", "1.8.1-4")
	auditU1StorePkg(t, storeRoot, "fd", "10.4.2-1")

	if err := generation.Build(map[string]string{
		"jq": "1.8.1", "fd": "10.4.2",
	}, galeDir, storeRoot); err != nil {
		t.Fatalf("Build: %v", err)
	}

	if !generationDrifted(galeDir, storeRoot,
		map[string]string{"jq": "1.8.1"}, nil) {
		t.Error("generationDrifted = false after removing fd " +
			"from config; its symlinks would stay on PATH")
	}
}
