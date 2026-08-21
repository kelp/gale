package main

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/provenance"
	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/store"
)

// gh#197 leftovers: gc no longer rebuilds a generation.
// A withdrawn recipe revision is not a version selector for gc.
//
// One package at two revisions throughout: 1.48.0-1 and 1.48.0-2.
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

func runGC(t *testing.T) error {
	t.Helper()
	dryRun = false
	t.Cleanup(func() { dryRun = false })
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

	active, err := generation.CurrentVersions(galeDir, storeRoot)
	if err != nil {
		t.Fatalf("reading the active generation: %v", err)
	}
	if active[issue197Pkg] != issue197Rev2 {
		t.Errorf("gc must not rebuild; active = %v, want %s",
			active, issue197Rev2)
	}
	_ = fetchDir
	_ = lockPath
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

	if err := os.WriteFile(
		filepath.Join(os.Getenv("HOME"), ".gale", "projects"),
		[]byte(proj+"\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	if err := runGC(t); err != nil {
		t.Fatalf("gc: %v", err)
	}

	active, err := generation.CurrentVersions(projGaleDir, storeRoot)
	if err != nil {
		t.Fatalf("reading the active generation: %v", err)
	}
	if active[issue197Pkg] != issue197Rev2 {
		t.Errorf("gc must not rebuild; active = %v, want %s",
			active, issue197Rev2)
	}
	_ = fetchDir
	_ = lockPath
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

	if err := runGC(t); err != nil {
		t.Fatalf("gc must not rebuild, so an unusable lock is not an error: %v", err)
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
