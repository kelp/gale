package installer

import (
	"bytes"
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

// TestReplaceGuardLeavesAPreRevisionDirAlone: an install predating
// revisions lives in a BARE directory, so pkg@1.0-1 resolves to
// pkg/1.0 while the canonical destination does not exist.
//
// Nothing is replaced there, so nothing is guarded and nothing is
// destroyed. The install lands in the canonical directory and the
// bare one is left exactly as it was. Deleting it would be a
// relocation, and a relocation is not one scope's to make: other
// scopes' generations link that path, and no per-scope command can
// repair their symlinks afterwards.
func TestReplaceGuardLeavesAPreRevisionDirAlone(t *testing.T) {
	storeRoot := t.TempDir()
	_, marker := seedOccupied(t, storeRoot, "oldpkg", "1.0")
	canonical := filepath.Join(storeRoot, "oldpkg", "1.0-1")

	r, closeSrv := fallbackRecipe(t, "oldpkg")
	defer closeSrv()

	calls := 0
	inst := &Installer{
		Store:             store.NewStore(storeRoot),
		BinaryFallbackLog: &bytes.Buffer{},
		ReplaceGuard: func(Replacement) error {
			calls++
			return nil
		},
	}

	if _, err := inst.Reinstall(r); err != nil {
		t.Fatalf("Reinstall: %v", err)
	}
	if calls != 0 {
		t.Errorf("guard called %d times; an install into a free "+
			"canonical dir replaces nothing", calls)
	}
	kept, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("the pre-revision dir was destroyed: %v", err)
	}
	if string(kept) != "old" {
		t.Errorf("marker holds %q, want %q", kept, "old")
	}
	if _, err := os.Stat(canonical); err != nil {
		t.Errorf("no canonical dir after the install: %v", err)
	}
}

// TestReplaceGuardRefusalKeepsTheCanonicalDir: the guard runs before
// the staged artifact overwrites an occupied canonical directory, so
// a refusal leaves the previous bytes exactly as they were.
//
// A caller that deleted the directory itself and then installed
// could not offer this. A refused, failed, or interrupted fetch
// would leave the identity absent, having destroyed bytes on the
// strength of a prediction about what would replace them.
func TestReplaceGuardRefusalKeepsTheCanonicalDir(t *testing.T) {
	storeRoot := t.TempDir()
	_, marker := seedOccupied(t, storeRoot, "vetopkg", "1.0-1")

	r, closeSrv := fallbackRecipe(t, "vetopkg")
	defer closeSrv()

	inst := &Installer{
		Store:             store.NewStore(storeRoot),
		BinaryFallbackLog: &bytes.Buffer{},
		ReplaceGuard: func(Replacement) error {
			return fmt.Errorf("scope disagrees: %w", errTestVeto)
		},
	}

	if _, err := inst.Reinstall(r); !errors.Is(err, errTestVeto) {
		t.Fatalf("err = %v, want the guard's refusal", err)
	}
	kept, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("canonical dir gone despite refusal: %v", err)
	}
	if string(kept) != "old" {
		t.Errorf("canonical dir holds %q, want %q: a refusal replaces "+
			"nothing", kept, "old")
	}
}

// TestReplaceGuardSeesTheCommittedArtifact: the guard is called with
// the bytes actually about to land, never with what the recipe
// predicted.
//
// The distinction is the whole reason the check lives here. An
// unlocked install falls back from a failed binary fetch to a source
// build, so a veto decided on the recipe's declared binary SHA would
// clear a hash no scope agreed to and then commit a different one.
func TestReplaceGuardSeesTheCommittedArtifact(t *testing.T) {
	storeRoot := t.TempDir()
	canonical, _ := seedOccupied(t, storeRoot, "seenpkg", "1.0-1")

	r, closeSrv := fallbackRecipe(t, "seenpkg")
	defer closeSrv()

	var got Replacement
	stagingReadable := false
	calls := 0
	inst := &Installer{
		Store:             store.NewStore(storeRoot),
		BinaryFallbackLog: &bytes.Buffer{},
		ReplaceGuard: func(rep Replacement) error {
			calls++
			got = rep
			// Checked here, while the guard holds the locks: the
			// staging dir is renamed away the moment it returns, and a
			// guard that cannot read it cannot inspect the candidate.
			_, err := os.Stat(rep.StagingDir)
			stagingReadable = err == nil
			return nil
		},
	}

	result, err := inst.Reinstall(r)
	if err != nil {
		t.Fatalf("Reinstall: %v", err)
	}
	if calls != 1 {
		t.Fatalf("guard called %d times, want exactly 1", calls)
	}
	if got.CanonicalDir != canonical {
		t.Errorf("guard saw dir %q, want the canonical dir %q",
			got.CanonicalDir, canonical)
	}
	// The staging dir is carried so a guard can inspect what is about
	// to land, not merely be told about it.
	if got.StagingDir == "" || got.StagingDir == canonical {
		t.Errorf("guard saw staging dir %q, want a sibling staging dir",
			got.StagingDir)
	}
	if !stagingReadable {
		t.Error("the staging dir named to the guard was not readable " +
			"while the guard ran, so a guard cannot inspect the candidate")
	}
	if got.Result.Method != MethodSource {
		t.Errorf("guard saw method %q, want source: the binary fetch "+
			"404s and the install falls back", got.Result.Method)
	}
	if got.Result.SHA256 == declaredBinarySHA {
		t.Error("guard saw the recipe's declared binary SHA, which is " +
			"a prediction; it must see the artifact being committed")
	}
	if got.Result.SHA256 != result.SHA256 {
		t.Errorf("guard saw SHA %q, committed %q",
			got.Result.SHA256, result.SHA256)
	}
	if got.Result.Name != "seenpkg" || got.Result.Version != "1.0" {
		t.Errorf("guard saw %s@%s, want seenpkg@1.0",
			got.Result.Name, got.Result.Version)
	}
}

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
		Store:             store.NewStore(storeRoot),
		BinaryFallbackLog: &bytes.Buffer{},
		BinaryOnly:        true,
	}
	if _, err := inst.Install(r); err == nil {
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
