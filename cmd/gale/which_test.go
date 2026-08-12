package main

import (
	"os"
	"path/filepath"
	"slices"
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
			filepath.Join(galeDir, "current"),
		); err != nil {
			t.Fatal(err)
		}

		name, version, resolved, err := resolveWhich(
			"jq", galeDir, storeRoot,
		)
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
			filepath.Join(galeDir, "current"),
		); err != nil {
			t.Fatal(err)
		}

		_, _, _, err := resolveWhich(
			"nonexistent", galeDir, storeRoot,
		)
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
			filepath.Join(galeDir, "current"),
		); err != nil {
			t.Fatal(err)
		}

		_, _, _, err := resolveWhich(
			"broken", galeDir, storeRoot,
		)
		if err == nil {
			t.Fatal("expected error for broken symlink")
		}
	})

	t.Run("rejects store path missing bin segment", func(t *testing.T) {
		tmp := t.TempDir()
		storeRoot := filepath.Join(tmp, "pkg")
		galeDir := tmp

		// Create a malformed store entry without bin/
		// segment: pkg/jq/1.8.1/jq (missing bin/).
		badDir := filepath.Join(storeRoot, "jq", "1.8.1")
		if err := os.MkdirAll(badDir, 0o755); err != nil {
			t.Fatal(err)
		}
		binPath := filepath.Join(badDir, "jq")
		if err := os.WriteFile(
			binPath, []byte("fake"), 0o755,
		); err != nil {
			t.Fatal(err)
		}

		// Create generation pointing to the bad path.
		genBinDir := filepath.Join(
			galeDir, "gen", "1", "bin",
		)
		if err := os.MkdirAll(genBinDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(binPath,
			filepath.Join(genBinDir, "jq")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(
			filepath.Join(galeDir, "gen", "1"),
			filepath.Join(galeDir, "current"),
		); err != nil {
			t.Fatal(err)
		}

		_, _, _, err := resolveWhich(
			"jq", galeDir, storeRoot,
		)
		if err == nil {
			t.Fatal("expected error for path missing bin/ segment")
		}
	})

	t.Run("git hash version", func(t *testing.T) {
		tmp := t.TempDir()
		storeRoot := filepath.Join(tmp, "pkg")
		galeDir := tmp

		// Create store with git hash version.
		binDir := filepath.Join(
			storeRoot, "gale", "d871cf2", "bin",
		)
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		binPath := filepath.Join(binDir, "gale")
		if err := os.WriteFile(
			binPath, []byte("fake"), 0o755,
		); err != nil {
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
			filepath.Join(galeDir, "current"),
		); err != nil {
			t.Fatal(err)
		}

		name, version, _, err := resolveWhich(
			"gale", galeDir, storeRoot,
		)
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

// TestOtherProvidersReportsShadowedPackage covers gh#190's reporting
// half: a [bin] override leaves the losing package installed with its
// binary unreachable, and `which` is where a user asks why.
func TestOtherProvidersReportsShadowedPackage(t *testing.T) {
	galeDir, storeRoot := setupGCHome(t)

	alphaDir := mkStorePkg(t, storeRoot, "alpha", "1.0")
	betaDir := mkStorePkg(t, storeRoot, "beta", "1.0")
	addStoreBin(t, alphaDir, "foo")
	addStoreBin(t, betaDir, "foo")
	mkStorePkg(t, storeRoot, "gamma", "1.0")

	writeGlobalConfig(t, galeDir,
		"[packages]\nalpha = \"1.0\"\nbeta = \"1.0\"\ngamma = \"1.0\"\n\n"+
			"[bin]\nfoo = \"beta\"\n")
	if err := rebuildGeneration(
		galeDir, storeRoot, filepath.Join(galeDir, "gale.toml"), nil,
	); err != nil {
		t.Fatalf("rebuildGeneration: %v", err)
	}

	name, _, _, err := resolveWhich("foo", galeDir, storeRoot)
	if err != nil {
		t.Fatalf("resolveWhich: %v", err)
	}
	if name != "beta" {
		t.Fatalf("winner = %q, want beta", name)
	}

	got := otherProviders("foo", name, galeDir, storeRoot)
	want := []string{"alpha"}
	if !slices.Equal(got, want) {
		t.Errorf("otherProviders = %v, want %v — gamma ships no foo, "+
			"beta is the winner", got, want)
	}
}
