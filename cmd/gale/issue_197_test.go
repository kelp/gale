package main

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/provenance"
	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/store"
)

// gh#197: gc rebuilds a generation from the recipe and the store,
// which after a revision bump or a withdrawn revision is a second
// version selector. It can therefore activate a version the scope's
// lock does not name, and at global scope design §12 runs no
// activation gate, so nothing downstream ever notices.
//
// One package at two revisions throughout: 1.48.0-1 and 1.48.0-2.
// Which of the two the lock names differs per test: gc's rebuild
// resolves the recipe and walks DOWN to a withdrawn revision.
const (
	issue197Pkg  = "just"
	issue197Ver  = "1.48.0"
	issue197Rev1 = issue197Ver + "-1"
	issue197Rev2 = issue197Ver + "-2"
)

// issue197Home lays out a gale home whose store holds both revisions
// and returns (galeDir, storeRoot). Both revision dirs are populated,
// so store resolution counts each as a real install rather than as
// in-flight debris.
func issue197Home(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GALE_OFFLINE", "1")
	galeDir := filepath.Join(home, ".gale")
	storeRoot := filepath.Join(galeDir, "pkg")
	mkStorePkg(t, storeRoot, issue197Pkg, issue197Rev1)
	mkStorePkg(t, storeRoot, issue197Pkg, issue197Rev2)
	return galeDir, storeRoot
}

// writeFile writes one fixture file, creating its directory.
func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// justAtRevision is the pin resolver for the fixture package at one
// recipe revision. It seeds the generation the way an install at that
// revision leaves it.
func justAtRevision(rev int) versionedRecipeResolver {
	return func(name, version string) (*recipe.Recipe, error) {
		if name != issue197Pkg || version != issue197Ver {
			return nil, fmt.Errorf("unexpected pin %s@%s", name, version)
		}
		return &recipe.Recipe{Package: recipe.Package{
			Name: issue197Pkg, Version: issue197Ver, Revision: rev,
		}}, nil
	}
}

// writeJustRecipes writes the letter-bucketed local recipe tree gc
// resolves through --recipes and returns its path. gc builds its own
// resolver inside RunE, so a file is the only way to tell the command
// which revision the recipe currently offers.
func writeJustRecipes(t *testing.T, rev int) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, issue197Pkg[:1], issue197Pkg+".toml"),
		strings.Join([]string{
			`[package]`,
			`name = "` + issue197Pkg + `"`,
			`version = "` + issue197Ver + `"`,
			fmt.Sprintf("revision = %d", rev),
			``,
			`[source]`,
			`url = "https://example.invalid/just.tar.gz"`,
			`sha256 = "deadbeef"`,
		}, "\n"))
	return dir
}

// runGC drives the real command against a local recipe tree that
// offers revision 1 — the recipe after the bump was withdrawn — so the
// resolver, the superseded-orphan gate and the rebuild are the
// production ones.
func runGC(t *testing.T) error {
	t.Helper()
	gcRecipes = writeJustRecipes(t, 1)
	dryRun = false
	t.Cleanup(func() {
		gcRecipes = ""
		dryRun = false
	})
	return gcCmd.RunE(gcCmd, nil)
}

func writeIssue197V2(t *testing.T, storeRoot, lockPath string) string {
	t.Helper()
	dest, err := store.NewStore(storeRoot).FetchPath(issue197Pkg, issue197Ver, shaX)
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dest, "bin", issue197Pkg)
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("fetch-"+issue197Ver+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.WriteV2(lockPath, &lockfile.V2{
		Version: lockfile.SchemaV2,
		Targets: lockfile.Targets{
			Default: &lockfile.Target{Roots: []string{issue197Pkg + "@" + issue197Ver}},
		},
		Packages: map[string]lockfile.V2Package{
			issue197Pkg + "@" + issue197Ver: {
				Artifacts: map[string]lockfile.V2Artifact{
					currentPlatform(): {
						URL:    "https://example.invalid/just",
						Format: "binary",
						SHA256: shaX,
						Method: provenance.MethodFetch,
					},
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	return dest
}

func assertGenerationMatchesLock(
	t *testing.T, galeDir, storeRoot, lockPath, fetchDir string, want map[string]string,
) {
	t.Helper()
	installed, err := generation.CurrentVersions(galeDir, storeRoot)
	if err != nil {
		t.Fatalf("reading the active generation: %v", err)
	}
	if _, err := lockfile.ReadV2(lockPath); err != nil {
		t.Fatalf("loading %s: %v", lockPath, err)
	}
	if !maps.Equal(installed, want) {
		t.Errorf("active generation = %v, want lock %v", installed, want)
	}
	dirs, err := generation.CurrentStoreDirs(galeDir, storeRoot)
	if err != nil {
		t.Fatalf("current store dirs: %v", err)
	}
	if filepath.Clean(dirs[issue197Pkg]) != filepath.Clean(fetchDir) {
		t.Errorf("linked %q, want fetch %q", dirs[issue197Pkg], fetchDir)
	}
}

// TestGCRebuildDoesNotActivateAnUnlockedRevision drives gh#197's gc
// half at global scope.
//
// The store holds the revision the lock names (1.48.0-2, installed
// while the recipe offered it) and a lower one. The recipe then
// withdraws revision 2, which is precisely the state gc's
// superseded-orphan rebuild exists for (gh#137): it relinks the
// revision the RECIPE offers. Under a lock that is a version
// substitution, and global scope has no activation gate to catch it.
func TestGCRebuildDoesNotActivateAnUnlockedRevision(t *testing.T) {
	galeDir, storeRoot := issue197Home(t)
	t.Chdir(t.TempDir()) // neutral cwd: no project scope here
	configPath := filepath.Join(galeDir, "gale.toml")
	lockPath := filepath.Join(galeDir, "gale.lock")
	writeFile(t, configPath, "[packages]\n"+issue197Pkg+" = \""+issue197Ver+"\"\n")
	if err := generation.Build(
		map[string]string{issue197Pkg: issue197Rev2}, galeDir, storeRoot,
	); err != nil {
		t.Fatalf("seeding the generation: %v", err)
	}
	fetchDir := writeIssue197V2(t, storeRoot, lockPath)

	if err := runGC(t); err != nil {
		t.Fatalf("gc: %v", err)
	}

	assertGenerationMatchesLock(t, galeDir, storeRoot, lockPath, fetchDir,
		map[string]string{issue197Pkg: issue197Ver})
}

// TestGCRebuildHonorsAProjectLock is the same defect one scope over.
// Project scope has an activation gate, so drift here is caught later
// rather than never — but caught means the project's shell breaks on
// the next cd, having been broken by gc.
func TestGCRebuildHonorsAProjectLock(t *testing.T) {
	_, storeRoot := issue197Home(t)
	proj := t.TempDir()
	t.Chdir(proj)
	projGaleDir := filepath.Join(proj, ".gale")
	configPath := filepath.Join(proj, "gale.toml")
	lockPath := filepath.Join(proj, "gale.lock")
	writeFile(t, configPath, "[packages]\n"+issue197Pkg+" = \""+issue197Ver+"\"\n")
	if err := generation.Build(
		map[string]string{issue197Pkg: issue197Rev2}, projGaleDir, storeRoot,
	); err != nil {
		t.Fatalf("seeding the project generation: %v", err)
	}
	fetchDir := writeIssue197V2(t, storeRoot, lockPath)

	if err := runGC(t); err != nil {
		t.Fatalf("gc: %v", err)
	}

	assertGenerationMatchesLock(t, projGaleDir, storeRoot, lockPath, fetchDir,
		map[string]string{issue197Pkg: issue197Ver})
}

// issue197UnusableLockFixture is the shared setup for the refusal
// tests: the gc rebuild fires, and the scope's lock is present in a
// schema this build cannot model. Returns the gale dir and store root.
func issue197UnusableLockFixture(t *testing.T) (string, string) {
	t.Helper()
	galeDir, storeRoot := issue197Home(t)
	t.Chdir(t.TempDir())
	configPath := filepath.Join(galeDir, "gale.toml")
	writeFile(t, configPath, "[packages]\n"+issue197Pkg+" = \""+issue197Ver+"\"\n")
	if err := rebuildGeneration(
		galeDir, storeRoot, configPath, justAtRevision(2),
	); err != nil {
		t.Fatalf("seeding the generation: %v", err)
	}
	writeFile(t, filepath.Join(galeDir, "gale.lock"), legacyLockBody)
	return galeDir, storeRoot
}

// TestGCRefusesRebuildOnAnUnusableLock pins the third state. A lock
// that is present and cannot be modeled leaves gc unable to tell
// whether its rebuild would publish a version the lock names, and
// rebuilding anyway is the bug this issue is about. It refuses that
// scope's rebuild and names the command that ends the state.
func TestGCRefusesRebuildOnAnUnusableLock(t *testing.T) {
	galeDir, storeRoot := issue197UnusableLockFixture(t)

	err := runGC(t)
	if err == nil {
		t.Fatal("gc must refuse to rebuild against a lock it cannot model")
	}
	if code := exitCodeFor(err); code != exitLockUnusable {
		t.Errorf("exit code = %d, want %d (%v)", code, exitLockUnusable, err)
	}
	if !strings.Contains(err.Error(), "gale fetch-adopt") {
		t.Errorf("the refusal must name fetch-adopt, got: %v", err)
	}
	if strings.Contains(err.Error(), "--force") {
		t.Errorf("the refusal must not advertise --force rebuild, got: %v", err)
	}
	// The refusal is a refusal: the generation is left exactly as it
	// was, not half-rebuilt.
	active, cErr := generation.CurrentVersions(galeDir, storeRoot)
	if cErr != nil {
		t.Fatalf("reading the active generation: %v", cErr)
	}
	if active[issue197Pkg] != issue197Rev2 {
		t.Errorf("active generation = %v, want %s untouched",
			active, issue197Rev2)
	}
}

// TestGCForceDoesNotRebuildPastUnusableLock: rebuild --force
// cannot walk past a lock that is present and cannot be modeled.
// Sweep --force is a different flag site.
func TestGCForceDoesNotRebuildPastUnusableLock(t *testing.T) {
	galeDir, storeRoot := issue197UnusableLockFixture(t)
	gcForce = true
	t.Cleanup(func() { gcForce = false })

	err := runGC(t)
	if err == nil {
		t.Fatal("gc --force must not rebuild past a present unusable lock")
	}
	if code := exitCodeFor(err); code != exitLockUnusable {
		t.Errorf("exit code = %d, want %d (%v)", code, exitLockUnusable, err)
	}
	if strings.Contains(err.Error(), "--force") {
		t.Errorf("refusal must not advertise --force rebuild, got: %v", err)
	}
	active, cErr := generation.CurrentVersions(galeDir, storeRoot)
	if cErr != nil {
		t.Fatalf("reading the active generation: %v", cErr)
	}
	if active[issue197Pkg] != issue197Rev2 {
		t.Errorf("active generation = %v, want %s untouched",
			active, issue197Rev2)
	}
}
