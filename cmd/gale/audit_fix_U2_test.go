package main

// Red-green test for gh#68 (U2 farm-reconcile audit unit):
// update and remove mutate gale.toml/gale.lock and then call
// the strict generation rebuild, which errors when the config
// lists any package whose store dir is absent — a state gale
// itself creates (`gale add` without sync, fresh clone,
// unsupported-platform skip). The config mutation lands but
// the generation never rotates, desyncing PATH from config.
// rebuildGeneration must tolerate the missing package
// (skip-with-warning) and still rotate the generation.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kelp/gale/internal/store"
)

func TestRebuildGenerationToleratesUninstalledPackage(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()
	configPath := filepath.Join(galeDir, "gale.toml")

	s := store.NewStore(storeRoot)
	dir, err := s.Create("foo", "1.0")
	if err != nil {
		t.Fatalf("store create: %v", err)
	}
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("create bin dir: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(binDir, "foo"), []byte("#!/bin/sh\n"), 0o755,
	); err != nil {
		t.Fatalf("create executable: %v", err)
	}

	// gale.toml lists foo (installed) and ghost (no store
	// dir) — the post-`gale add`/fresh-clone state.
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n  foo = \"1.0\"\n  ghost = \"2.0\"\n"),
		0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// The rebuild update/remove run after committing their
	// config mutation. It must not fail on ghost.
	if err := rebuildGeneration(galeDir, storeRoot, configPath); err != nil {
		t.Fatalf(
			"rebuildGeneration failed on a config package that "+
				"is not installed: %v", err,
		)
	}

	// The generation must have rotated and carry foo.
	if _, err := os.Lstat(
		filepath.Join(galeDir, "current", "bin", "foo"),
	); err != nil {
		t.Errorf("foo missing from rebuilt generation: %v", err)
	}
	if _, err := os.Lstat(
		filepath.Join(galeDir, "current", "bin", "ghost"),
	); err == nil {
		t.Error("ghost unexpectedly present in rebuilt generation")
	}
}
