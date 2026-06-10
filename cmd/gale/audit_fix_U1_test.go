package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/recipe"
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

// TestFinalizeRecipeInstallPinsExplicitOlderRevision is the repro
// for gh#65: `gale switch foo 1.2.3-1` (or `gale install
// foo@1.2.3-1`) while 1.2.3-2 is already in the append-only store.
// Writing the bare "1.2.3" to gale.toml makes generation build
// resolve to the highest on-disk revision (1.2.3-2), so the
// requested rollback revision never activates. The finalize path
// must pin the canonical "1.2.3-1" in gale.toml whenever the bare
// form would resolve to a different store dir than the revision
// being installed.
func TestFinalizeRecipeInstallPinsExplicitOlderRevision(t *testing.T) {
	tmp := t.TempDir()
	galeDir := filepath.Join(tmp, ".gale")
	storeRoot := filepath.Join(tmp, "pkg")
	configPath := filepath.Join(tmp, "gale.toml")

	if err := os.WriteFile(configPath,
		[]byte("[packages]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	auditU1StorePkg(t, storeRoot, "foo", "1.2.3-1")
	auditU1StorePkg(t, storeRoot, "foo", "1.2.3-2")

	ctx := &cmdContext{
		GalePath:  configPath,
		GaleDir:   galeDir,
		StoreRoot: storeRoot,
	}
	r := &recipe.Recipe{Package: recipe.Package{
		Name: "foo", Version: "1.2.3", Revision: 1,
	}}
	if err := ctx.FinalizeRecipeInstall(r, "deadbeef"); err != nil {
		t.Fatalf("FinalizeRecipeInstall: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"1.2.3-1"`) {
		t.Errorf("gale.toml = %q, want revision-qualified pin "+
			"\"1.2.3-1\" (gh#65: bare pin resolves to 1.2.3-2)",
			string(data))
	}

	target, err := os.Readlink(
		filepath.Join(galeDir, "gen", "1", "bin", "foo"),
	)
	if err != nil {
		t.Fatalf("foo missing from generation: %v", err)
	}
	wantFragment := filepath.Join("foo", "1.2.3-1", "bin", "foo")
	if !strings.Contains(target, wantFragment) {
		t.Errorf("foo symlink target = %q, want fragment %q "+
			"(requested revision must activate)", target, wantFragment)
	}
}

// TestFinalizeRecipeInstallKeepsBarePinForLatestRevision guards
// the other half of the gh#65 fix: a normal install — where the
// revision being installed IS what the bare version resolves to —
// must keep writing the bare form, so gale.toml entries continue
// to track recipe revision bumps automatically.
func TestFinalizeRecipeInstallKeepsBarePinForLatestRevision(t *testing.T) {
	tmp := t.TempDir()
	galeDir := filepath.Join(tmp, ".gale")
	storeRoot := filepath.Join(tmp, "pkg")
	configPath := filepath.Join(tmp, "gale.toml")

	if err := os.WriteFile(configPath,
		[]byte("[packages]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	auditU1StorePkg(t, storeRoot, "foo", "1.2.3-1")
	auditU1StorePkg(t, storeRoot, "foo", "1.2.3-2")

	ctx := &cmdContext{
		GalePath:  configPath,
		GaleDir:   galeDir,
		StoreRoot: storeRoot,
	}
	r := &recipe.Recipe{Package: recipe.Package{
		Name: "foo", Version: "1.2.3", Revision: 2,
	}}
	if err := ctx.FinalizeRecipeInstall(r, "deadbeef"); err != nil {
		t.Fatalf("FinalizeRecipeInstall: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `foo = "1.2.3"`) {
		t.Errorf("gale.toml = %q, want bare pin foo = \"1.2.3\"",
			string(data))
	}
}

// TestGenerationDriftedFalseWhenConfigUnchanged is the repro for
// gh#49: after a fresh build from an unchanged gale.toml, sync's
// drift check compared the bare config version ("1.8.1") against
// the active generation's store-dir basename ("1.8.1-4") and
// reported drift on every run — so every no-op `gale sync`
// rebuilt and re-swapped a new generation, churning history and
// silently undoing rollbacks.
func TestGenerationDriftedFalseWhenConfigUnchanged(t *testing.T) {
	tmp := t.TempDir()
	galeDir := filepath.Join(tmp, ".gale")
	storeRoot := filepath.Join(tmp, "pkg")

	auditU1StorePkg(t, storeRoot, "jq", "1.8.1-4")

	pkgs := map[string]string{"jq": "1.8.1"}
	if err := generation.Build(pkgs, galeDir, storeRoot); err != nil {
		t.Fatalf("Build: %v", err)
	}

	if generationDrifted(galeDir, storeRoot, pkgs) {
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
		map[string]string{"jq": "1.8.1"}) {
		t.Error("generationDrifted = false after removing fd " +
			"from config; its symlinks would stay on PATH")
	}
}
