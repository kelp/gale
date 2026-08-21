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

		got, err := resolveWhich(
			"jq", galeDir, storeRoot,
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.name != "jq" {
			t.Errorf("name = %q, want %q", got.name, "jq")
		}
		if got.version != "1.8.1" {
			t.Errorf("version = %q, want %q", got.version, "1.8.1")
		}
		// EvalSymlinks to handle macOS /var → /private/var.
		wantPath, _ := filepath.EvalSymlinks(binPath)
		if got.path != wantPath {
			t.Errorf("resolved = %q, want %q",
				got.path, wantPath)
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

		_, err := resolveWhich(
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

		_, err := resolveWhich(
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

		_, err := resolveWhich(
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

		got, err := resolveWhich(
			"gale", galeDir, storeRoot,
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.name != "gale" {
			t.Errorf("name = %q, want %q", got.name, "gale")
		}
		if got.version != "d871cf2" {
			t.Errorf("version = %q, want %q",
				got.version, "d871cf2")
		}
	})
}

// TestOtherProvidersReportsShadowedPackage covers a pre-upgrade
// generation: two packages both ship foo, only beta's copy is
// on PATH, and `which` reports the loser. A successful rebuild
// cannot produce this state — leftover [bin] no longer settles
// a collision — so the generation is assembled by hand.
func TestOtherProvidersReportsShadowedPackage(t *testing.T) {
	galeDir, storeRoot := setupGCHome(t)

	alphaDir := mkStorePkg(t, storeRoot, "alpha", "1.0")
	betaDir := mkStorePkg(t, storeRoot, "beta", "1.0")
	addStoreBin(t, alphaDir, "foo")
	addStoreBin(t, betaDir, "foo")
	gammaDir := mkStorePkg(t, storeRoot, "gamma", "1.0")

	mkActiveGen(
		t, galeDir, 1,
		filepath.Join(alphaDir, "bin", "alpha"),
		filepath.Join(betaDir, "bin", "beta"),
		filepath.Join(betaDir, "bin", "foo"),
		filepath.Join(gammaDir, "bin", "gamma"),
	)

	winner, err := resolveWhich("foo", galeDir, storeRoot)
	if err != nil {
		t.Fatalf("resolveWhich: %v", err)
	}
	if winner.name != "beta" {
		t.Fatalf("winner = %q, want beta", winner.name)
	}

	got := otherProviders("foo", winner.name, galeDir, storeRoot)
	want := []string{"alpha"}
	if !slices.Equal(got, want) {
		t.Errorf("otherProviders = %v, want %v — gamma ships no foo, "+
			"beta is the winner", got, want)
	}
}
