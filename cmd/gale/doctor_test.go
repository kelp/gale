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
