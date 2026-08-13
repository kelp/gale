package main

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/activation"
	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/recipe"
)

// gh#197: gc and doctor --repair rebuild a generation from the recipe
// and the store, which after a revision bump or a withdrawn revision
// is a second version selector. Either can therefore activate a
// version the scope's lock does not name, and at global scope design
// §12 runs no activation gate, so nothing downstream ever notices.
//
// One package at two revisions throughout: 1.48.0-1 and 1.48.0-2.
// Which of the two the lock names differs per test, because the two
// commands drift in opposite directions — gc's rebuild resolves the
// recipe and walks DOWN to a withdrawn revision, doctor's passes no
// resolver at all and walks UP to the highest revision on disk.
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

// writeHostScopeLock is writeScopeLock's overlay twin: it roots id
// under [targets.host.<host>] instead of [targets.default]. It is the
// only fixture that can prove a rebuild asks the lock about the host
// it is running on — a rebuild querying the default target alone finds
// no root here, links nothing, and would otherwise pass the
// unlocked-versions check vacuously.
func writeHostScopeLock(t *testing.T, path, host, id, sha string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.WriteV1(path, &lockfile.V1{
		Version: lockfile.SchemaVersion,
		Targets: lockfile.Targets{
			Host: map[string]lockfile.Target{host: {Roots: []string{id}}},
		},
		Packages: map[string]lockfile.Package{
			id: {Artifacts: map[string]lockfile.Artifact{
				testPlatform: {SHA256: sha, Method: "binary"},
			}},
		},
	}); err != nil {
		t.Fatal(err)
	}
}

// assertGenerationMatchesLock is gh#197's invariant, asserted the way
// design §12 states it: the active generation may link only versions
// the scope's lock names.
//
// activation.UnlockedVersions makes that comparison and is the one
// implementation of it, so the test asks it rather than comparing
// strings of its own. want pins the other direction — a generation
// that links nothing satisfies UnlockedVersions vacuously, which is
// exactly what a rebuild reading the wrong lock target would produce.
func assertGenerationMatchesLock(
	t *testing.T, galeDir, storeRoot, lockPath string, want map[string]string,
) {
	t.Helper()
	installed, err := generation.CurrentVersions(galeDir, storeRoot)
	if err != nil {
		t.Fatalf("reading the active generation: %v", err)
	}
	v, err := lockfile.Load(lockPath)
	if err != nil {
		t.Fatalf("loading %s: %v", lockPath, err)
	}
	roots, err := v.V1.EffectiveRoots(config.CurrentHost())
	if err != nil {
		t.Fatalf("effective roots of %s: %v", lockPath, err)
	}
	if unlocked := activation.UnlockedVersions(
		installed, roots, storeRoot,
	); len(unlocked) > 0 {
		t.Errorf(
			"the active generation links %v, which %s does not name",
			unlocked, lockPath,
		)
	}
	if !maps.Equal(installed, want) {
		t.Errorf("active generation = %v, want %v", installed, want)
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
	writeScopeLock(t, lockPath, issue197Pkg+"@"+issue197Rev2, shaX)

	// Seed: the generation links what the lock names, as an install
	// under the then-current recipe revision left it.
	if err := rebuildGeneration(
		galeDir, storeRoot, configPath, justAtRevision(2),
	); err != nil {
		t.Fatalf("seeding the generation: %v", err)
	}

	if err := runGC(t); err != nil {
		t.Fatalf("gc: %v", err)
	}

	assertGenerationMatchesLock(t, galeDir, storeRoot, lockPath,
		map[string]string{issue197Pkg: issue197Rev2})
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
	writeScopeLock(t, lockPath, issue197Pkg+"@"+issue197Rev2, shaX)

	if err := rebuildGeneration(
		projGaleDir, storeRoot, configPath, justAtRevision(2),
	); err != nil {
		t.Fatalf("seeding the project generation: %v", err)
	}

	if err := runGC(t); err != nil {
		t.Fatalf("gc: %v", err)
	}

	assertGenerationMatchesLock(t, projGaleDir, storeRoot, lockPath,
		map[string]string{issue197Pkg: issue197Rev2})
}

// TestDoctorRepairDoesNotActivateAnUnlockedRevision drives gh#197's
// doctor half. repairDoctor passes no pin resolver, so the bare pin in
// gale.toml goes through store.ResolveDir's bare→highest-revision
// rule: 1.48.0 resolves to 1.48.0-2 while the lock names 1.48.0-1.
// The higher revision is an orphan — installed for another scope, or
// left by a rolled-back install — and repair puts it on PATH.
func TestDoctorRepairDoesNotActivateAnUnlockedRevision(t *testing.T) {
	galeDir, storeRoot := issue197Home(t)
	cwd := t.TempDir()
	t.Chdir(cwd)
	configPath := filepath.Join(galeDir, "gale.toml")
	lockPath := filepath.Join(galeDir, "gale.lock")
	writeFile(t, configPath, "[packages]\n"+issue197Pkg+" = \""+issue197Ver+"\"\n")
	writeScopeLock(t, lockPath, issue197Pkg+"@"+issue197Rev1, shaX)

	if err := rebuildGeneration(
		galeDir, storeRoot, configPath, justAtRevision(1),
	); err != nil {
		t.Fatalf("seeding the generation: %v", err)
	}

	if err := repairDoctor(&doctorContext{
		galeDir:   galeDir,
		storeRoot: storeRoot,
		cwd:       cwd,
		out:       output.New(os.Stderr, false),
	}); err != nil {
		t.Fatalf("doctor --repair: %v", err)
	}

	assertGenerationMatchesLock(t, galeDir, storeRoot, lockPath,
		map[string]string{issue197Pkg: issue197Rev1})
}

// TestDoctorRepairHonorsAHostLockedRoot covers the third scope. The
// package is declared under [hosts.<host>.packages] and rooted under
// the lock's matching host target, so a repair that asked the lock
// about the wrong host would find no root, link nothing, and drop the
// package off PATH.
func TestDoctorRepairHonorsAHostLockedRoot(t *testing.T) {
	const host = "issue197-host"
	t.Setenv("GALE_HOST", host)
	galeDir, storeRoot := issue197Home(t)
	cwd := t.TempDir()
	t.Chdir(cwd)
	configPath := filepath.Join(galeDir, "gale.toml")
	lockPath := filepath.Join(galeDir, "gale.lock")
	writeFile(t, configPath, "[hosts."+host+".packages]\n"+
		issue197Pkg+" = \""+issue197Ver+"\"\n")
	writeHostScopeLock(t, lockPath, host, issue197Pkg+"@"+issue197Rev1, shaX)

	if err := rebuildGeneration(
		galeDir, storeRoot, configPath, justAtRevision(1),
	); err != nil {
		t.Fatalf("seeding the generation: %v", err)
	}

	if err := repairDoctor(&doctorContext{
		galeDir:   galeDir,
		storeRoot: storeRoot,
		cwd:       cwd,
		out:       output.New(os.Stderr, false),
	}); err != nil {
		t.Fatalf("doctor --repair: %v", err)
	}

	assertGenerationMatchesLock(t, galeDir, storeRoot, lockPath,
		map[string]string{issue197Pkg: issue197Rev1})
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
	writeFile(t, filepath.Join(galeDir, "gale.lock"), legacyLockBody)
	if err := rebuildGeneration(
		galeDir, storeRoot, configPath, justAtRevision(2),
	); err != nil {
		t.Fatalf("seeding the generation: %v", err)
	}
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
	if !strings.Contains(err.Error(), "gale lock --refresh") {
		t.Errorf("the refusal must name the remedy, got: %v", err)
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

// TestGCForceRebuildsDespiteAnUnusableLock is the escape hatch, the
// same shape --force already gives the sweep (gh#188): a user whose
// lock is beyond repair must still be able to run gc. It warns and
// rebuilds unlocked, which here means relinking the revision the
// recipe offers.
func TestGCForceRebuildsDespiteAnUnusableLock(t *testing.T) {
	galeDir, storeRoot := issue197UnusableLockFixture(t)
	gcForce = true
	t.Cleanup(func() { gcForce = false })

	var err error
	stderr := captureStderr(t, func() { err = runGC(t) })
	if err != nil {
		t.Fatalf("gc --force must rebuild anyway: %v", err)
	}
	if !strings.Contains(stderr, "gale.lock") {
		t.Errorf("gc --force must warn about the lock it ignored, got: %q", stderr)
	}
	active, cErr := generation.CurrentVersions(galeDir, storeRoot)
	if cErr != nil {
		t.Fatalf("reading the active generation: %v", cErr)
	}
	if active[issue197Pkg] != issue197Rev1 {
		t.Errorf("active generation = %v, want the unlocked rebuild's %s",
			active, issue197Rev1)
	}
}

// issue197RepairContext is a doctor context over the fixture home, cwd
// taken from the fixture's own chdir.
func issue197RepairContext(t *testing.T, galeDir, storeRoot string) *doctorContext {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return &doctorContext{
		galeDir:   galeDir,
		storeRoot: storeRoot,
		cwd:       cwd,
		out:       output.New(os.Stderr, false),
	}
}

// TestDoctorRepairRefusesOnAnUnusableLock is doctor's half of the same
// rule. Repair rebuilds unconditionally — it has no superseded-orphan
// gate — so the refusal is what stops it publishing a version nothing
// can check.
func TestDoctorRepairRefusesOnAnUnusableLock(t *testing.T) {
	galeDir, storeRoot := issue197UnusableLockFixture(t)

	err := repairDoctor(issue197RepairContext(t, galeDir, storeRoot))
	if err == nil {
		t.Fatal("doctor --repair must refuse a lock it cannot model")
	}
	if code := exitCodeFor(err); code != exitLockUnusable {
		t.Errorf("exit code = %d, want %d (%v)", code, exitLockUnusable, err)
	}
	if !strings.Contains(err.Error(), "gale lock --refresh") {
		t.Errorf("the refusal must name the remedy, got: %v", err)
	}
}

// TestDoctorRepairForceRebuildsDespiteAnUnusableLock is repair's
// escape hatch, `gale doctor --repair --force`. Repair is the command
// a user reaches for when the machine is broken, so the refusal above
// must not be the end of the road.
func TestDoctorRepairForceRebuildsDespiteAnUnusableLock(t *testing.T) {
	galeDir, storeRoot := issue197UnusableLockFixture(t)
	doctorForce = true
	t.Cleanup(func() { doctorForce = false })

	if err := repairDoctor(
		issue197RepairContext(t, galeDir, storeRoot),
	); err != nil {
		t.Fatalf("doctor --repair --force must repair anyway: %v", err)
	}
	active, err := generation.CurrentVersions(galeDir, storeRoot)
	if err != nil {
		t.Fatalf("reading the active generation: %v", err)
	}
	// Repair passes no pin resolver, so the unlocked rebuild takes
	// store.ResolveDir's bare→highest-revision answer.
	if active[issue197Pkg] != issue197Rev2 {
		t.Errorf("active generation = %v, want %s", active, issue197Rev2)
	}
}
