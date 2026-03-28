package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveWhich(t *testing.T) {
	t.Run("resolves binary to package", func(t *testing.T) {
		tmp := t.TempDir()
		storeRoot := filepath.Join(tmp, "pkg")
		galeDir := tmp

		// Create store: pkg/jq/1.8.1/bin/jq
		binDir := filepath.Join(storeRoot, "jq", "1.8.1", "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		binPath := filepath.Join(binDir, "jq")
		if err := os.WriteFile(binPath, []byte("fake"), 0o755); err != nil {
			t.Fatal(err)
		}

		// Create generation: gen/1/bin/jq → store path
		genBinDir := filepath.Join(galeDir, "gen", "1", "bin")
		if err := os.MkdirAll(genBinDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(binPath,
			filepath.Join(genBinDir, "jq")); err != nil {
			t.Fatal(err)
		}

		// Create current → gen/1
		if err := os.Symlink(
			filepath.Join(galeDir, "gen", "1"),
			filepath.Join(galeDir, "current")); err != nil {
			t.Fatal(err)
		}

		name, version, resolved, err := resolveWhich(
			"jq", galeDir, storeRoot)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if name != "jq" {
			t.Errorf("name = %q, want %q", name, "jq")
		}
		if version != "1.8.1" {
			t.Errorf("version = %q, want %q", version, "1.8.1")
		}
		// EvalSymlinks to handle macOS /var → /private/var.
		wantPath, _ := filepath.EvalSymlinks(binPath)
		if resolved != wantPath {
			t.Errorf("resolved = %q, want %q",
				resolved, wantPath)
		}
	})

	t.Run("binary not found", func(t *testing.T) {
		tmp := t.TempDir()
		storeRoot := filepath.Join(tmp, "pkg")
		galeDir := tmp

		// Create empty generation.
		genBinDir := filepath.Join(galeDir, "gen", "1", "bin")
		if err := os.MkdirAll(genBinDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(
			filepath.Join(galeDir, "gen", "1"),
			filepath.Join(galeDir, "current")); err != nil {
			t.Fatal(err)
		}

		_, _, _, err := resolveWhich(
			"nonexistent", galeDir, storeRoot)
		if err == nil {
			t.Fatal("expected error for missing binary")
		}
	})

	t.Run("broken symlink", func(t *testing.T) {
		tmp := t.TempDir()
		storeRoot := filepath.Join(tmp, "pkg")
		galeDir := tmp

		// Create generation with broken symlink.
		genBinDir := filepath.Join(galeDir, "gen", "1", "bin")
		if err := os.MkdirAll(genBinDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("/nonexistent/path",
			filepath.Join(genBinDir, "broken")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(
			filepath.Join(galeDir, "gen", "1"),
			filepath.Join(galeDir, "current")); err != nil {
			t.Fatal(err)
		}

		_, _, _, err := resolveWhich(
			"broken", galeDir, storeRoot)
		if err == nil {
			t.Fatal("expected error for broken symlink")
		}
	})

	t.Run("git hash version", func(t *testing.T) {
		tmp := t.TempDir()
		storeRoot := filepath.Join(tmp, "pkg")
		galeDir := tmp

		// Create store with git hash version.
		binDir := filepath.Join(
			storeRoot, "gale", "d871cf2", "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		binPath := filepath.Join(binDir, "gale")
		if err := os.WriteFile(
			binPath, []byte("fake"), 0o755); err != nil {
			t.Fatal(err)
		}

		genBinDir := filepath.Join(galeDir, "gen", "1", "bin")
		if err := os.MkdirAll(genBinDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(binPath,
			filepath.Join(genBinDir, "gale")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(
			filepath.Join(galeDir, "gen", "1"),
			filepath.Join(galeDir, "current")); err != nil {
			t.Fatal(err)
		}

		name, version, _, err := resolveWhich(
			"gale", galeDir, storeRoot)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if name != "gale" {
			t.Errorf("name = %q, want %q", name, "gale")
		}
		if version != "d871cf2" {
			t.Errorf("version = %q, want %q",
				version, "d871cf2")
		}
	})
}
