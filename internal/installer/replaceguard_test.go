package installer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/store"
)

// errTestVeto stands in for design §13's cross-scope refusal.
var errTestVeto = errors.New("test veto")

// seedOccupied creates an occupied canonical store dir holding one
// recognisable file, and returns the dir and that file's path.
func seedOccupied(t *testing.T, storeRoot, name, version string) (string, string) {
	t.Helper()
	dir := filepath.Join(storeRoot, name, version)
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(binDir, name)
	if err := os.WriteFile(marker, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, marker
}

// fallbackRecipe declares a binary that 404s and a source build that
// succeeds, so the install commits bytes the recipe's binary SHA
// does not describe.
func fallbackRecipe(t *testing.T, name string) (*recipe.Recipe, func()) {
	t.Helper()
	srcTar := createTestSourceTarGz(t)
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/source.tar.gz" {
				http.ServeFile(w, r, srcTar)
				return
			}
			http.NotFound(w, r)
		},
	))
	return &recipe.Recipe{
		Package: recipe.Package{Name: name, Version: "1.0"},
		Source: recipe.Source{
			URL: srv.URL + "/source.tar.gz", SHA256: hashFile(t, srcTar),
		},
		Binary: map[string]recipe.Binary{
			fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH): {
				URL:    srv.URL + "/missing.tar.zst",
				SHA256: declaredBinarySHA,
				Trust:  recipe.TrustSHA256Only,
			},
		},
		Build: recipe.Build{Steps: []string{
			"mkdir -p $PREFIX/bin",
			"echo '#!/bin/sh' > $PREFIX/bin/" + name,
			"chmod +x $PREFIX/bin/" + name,
		}},
	}, srv.Close
}

// declaredBinarySHA is what the recipe promises for the platform
// binary. The fetch 404s, so nothing with this hash is ever
// committed, which is the point of the test that names it.
const declaredBinarySHA = "1111111111111111111111111111111111111111111111111111111111111111"

// BinaryOnly refuses to demote a failed binary fetch to a source
// build, and leaves nothing behind.
//
// `gale migrate` needs this. It is a constrained replacement of
// BINARY-method directories (design §13), and every scope on the
// machine was cleared against the hash the recipe declares for that
// binary. A silent source build would commit bytes nobody was asked
// about — and for a pre-revision candidate, whose canonical
// destination is absent, no ReplaceGuard fires to catch it before
// the commit.
//
// The locked path already refuses the same demotion for the same
// reason, so this selects that behaviour rather than adding a
// second one.
func TestBinaryOnlyDoesNotFallBackToSource(t *testing.T) {
	storeRoot := t.TempDir()
	r, closeSrv := fallbackRecipe(t, "onlybin")
	defer closeSrv()

	inst := &Installer{
		Store:      store.NewStore(storeRoot),
		BinaryOnly: true,
	}
	if _, err := inst.Install(context.Background(), r); err == nil {
		t.Fatal("a failed binary fetch fell back to a source build")
	}
	// Nothing half-installed: the directory a source build would have
	// filled must not survive the refusal.
	if _, err := os.Lstat(
		filepath.Join(storeRoot, "onlybin", "1.0-1"),
	); err == nil {
		t.Error("the refused install left a store directory behind")
	}
}

// --- gh#183: the local-source path replaces bytes too ---

// localSourceRecipe copies the source tree's "marker" file into
// $PREFIX/bin, so the committed artifact IS the source content and a
// test can tell one build from another by reading the store.
func localSourceRecipe(name string) *recipe.Recipe {
	return &recipe.Recipe{
		Package: recipe.Package{Name: name, Version: "1.0"},
		Build: recipe.Build{Steps: []string{
			"mkdir -p $PREFIX/bin",
			"cp marker $PREFIX/bin/" + name,
			"chmod +x $PREFIX/bin/" + name,
		}},
	}
}

// localSourceDir returns a source tree whose marker file holds
// content.
func localSourceDir(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(dir, "marker"), []byte(content), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	return dir
}
