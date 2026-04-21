package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/store"
)

func TestRepairDoctorRebuildsGlobalGeneration(t *testing.T) {
	home := t.TempDir()
	galeDir := filepath.Join(home, ".gale")
	storeRoot := filepath.Join(home, ".gale", "pkg")
	configPath := filepath.Join(galeDir, "gale.toml")

	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n  jq = \"1.8.1\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := store.NewStore(storeRoot)
	pkgDir, err := s.Create("jq", "1.8.1")
	if err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(pkgDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "jq"),
		[]byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx := &doctorContext{
		galeDir:   galeDir,
		storeRoot: storeRoot,
		cwd:       home,
		out:       output.NewWithOptions(&bytes.Buffer{}, output.Options{}),
	}

	if err := repairDoctor(ctx); err != nil {
		t.Fatalf("repairDoctor: %v", err)
	}

	if _, err := os.Lstat(filepath.Join(galeDir, "current", "bin", "jq")); err != nil {
		t.Fatalf("jq symlink missing after repair: %v", err)
	}
}

// TestCheckOrphansIgnoresResolvedRevisions verifies that when
// config carries a bare version (`bat = "0.26.1"`) and the
// store holds the canonical revision dir (`bat/0.26.1-2`),
// checkOrphans does NOT flag the active package as orphaned.
// Before the fix, checkOrphans built the referenced set with
// the bare config key and compared against the store's revision
// key — strings never matched, so every active package looked
// orphaned and the count was wildly inflated.
func TestCheckOrphansIgnoresResolvedRevisions(t *testing.T) {
	home := t.TempDir()
	galeDir := filepath.Join(home, ".gale")
	storeRoot := filepath.Join(galeDir, "pkg")

	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(galeDir, "gale.toml"),
		[]byte("[packages]\n  bat = \"0.26.1\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	s := store.NewStore(storeRoot)
	pkgDir, err := s.Create("bat", "0.26.1-2")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(
		filepath.Join(pkgDir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(pkgDir, "bin", "bat"),
		[]byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	installed, err := s.List()
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	ctx := &doctorContext{
		galeDir:   galeDir,
		storeRoot: storeRoot,
		cwd:       home,
		store:     s,
		installed: installed,
		out:       output.NewWithOptions(&buf, output.Options{}),
	}

	if !checkOrphans(ctx) {
		t.Fatal("checkOrphans returned false (should warn-only)")
	}

	if bytes.Contains(buf.Bytes(), []byte("orphaned version(s)")) {
		t.Errorf("checkOrphans reported orphans for an active "+
			"package: %q", buf.String())
	}
}

// TestCheckOrphansCountsOldRevisions verifies that once an old
// revision is no longer referenced by config (bare version
// resolves to a newer revision), checkOrphans correctly flags
// the stale revision as orphaned.
func TestCheckOrphansCountsOldRevisions(t *testing.T) {
	home := t.TempDir()
	galeDir := filepath.Join(home, ".gale")
	storeRoot := filepath.Join(galeDir, "pkg")

	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(galeDir, "gale.toml"),
		[]byte("[packages]\n  jq = \"1.8.1\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	s := store.NewStore(storeRoot)
	// -3 is the highest, so bare jq = "1.8.1" resolves to it.
	// -2 is an old revision that should be flagged orphaned.
	for _, ver := range []string{"1.8.1-2", "1.8.1-3"} {
		d, err := s.Create("jq", ver)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(
			filepath.Join(d, "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(d, "bin", "jq"),
			[]byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	installed, err := s.List()
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	ctx := &doctorContext{
		galeDir:   galeDir,
		storeRoot: storeRoot,
		cwd:       home,
		store:     s,
		installed: installed,
		out:       output.NewWithOptions(&buf, output.Options{}),
	}

	if !checkOrphans(ctx) {
		t.Fatal("checkOrphans returned false (should warn-only)")
	}

	if !bytes.Contains(buf.Bytes(), []byte("1 orphaned version(s)")) {
		t.Errorf("expected 1 orphaned version (old jq-2), "+
			"got: %q", buf.String())
	}
}

func TestRepairDoctorRebuildsToolVersionsProjectGeneration(t *testing.T) {
	home := t.TempDir()
	galeDir := filepath.Join(home, ".gale")
	storeRoot := filepath.Join(home, ".gale", "pkg")
	globalConfig := filepath.Join(galeDir, "gale.toml")
	projectDir := filepath.Join(home, "project")
	projectGaleDir := filepath.Join(projectDir, ".gale")

	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(globalConfig, []byte("[packages]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".tool-versions"),
		[]byte("golang 1.26.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := store.NewStore(storeRoot)
	pkgDir, err := s.Create("go", "1.26.1")
	if err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(pkgDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "go"),
		[]byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx := &doctorContext{
		galeDir:   galeDir,
		storeRoot: storeRoot,
		cwd:       projectDir,
		out:       output.NewWithOptions(&bytes.Buffer{}, output.Options{}),
	}

	if err := repairDoctor(ctx); err != nil {
		t.Fatalf("repairDoctor: %v", err)
	}

	if _, err := os.Lstat(filepath.Join(projectGaleDir, "current", "bin", "go")); err != nil {
		t.Fatalf("go symlink missing after project repair: %v", err)
	}
}
