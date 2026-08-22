package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/generation"
)

// gcBranchFixture stages the shape every test in this file
// shares: jq@1.7-1 linked by gen/1, jq@1.8-1 linked by gen/2,
// `current` pointed at the caller's choice, and a global
// gale.toml pinning exactly one of the two versions — so the
// other survives only through a generation.
//
// With current at gen/1 this is the state a `gale generations
// rollback` leaves: gen/2 is the abandoned branch, retained so a
// roll-forward can return to it (gh#189).
//
// Both generations are created WITH their bin targets first, and
// `current` is re-pointed afterwards by a target-less mkActiveGen
// call. Creating the active generation last with its targets
// fails on the bin symlink that already exists (gh#252).
func gcBranchFixture(t *testing.T, current int, pin string) gcBranchEnv {
	t.Helper()
	env := gcBranchEnv{}
	env.galeDir, env.storeRoot = setupGCHome(t)
	writeGlobalConfig(
		t, env.galeDir, "[packages]\njq = \""+pin+"\"\n",
	)
	env.jq17 = mkStorePkg(t, env.storeRoot, "jq", "1.7-1")
	env.jq18 = mkStorePkg(t, env.storeRoot, "jq", "1.8-1")
	mkActiveGen(t, env.galeDir, 1, filepath.Join(env.jq17, "bin", "jq"))
	mkActiveGen(t, env.galeDir, 2, filepath.Join(env.jq18, "bin", "jq"))
	mkActiveGen(t, env.galeDir, current)
	return env
}

// gcThreeGenFixture is gcBranchFixture plus gen/1 → jq@1.6 so
// something sits below the keep-2 window when current is 3.
func gcThreeGenFixture(t *testing.T, current int, pin string) gcBranchEnv {
	t.Helper()
	env := gcBranchEnv{}
	env.galeDir, env.storeRoot = setupGCHome(t)
	writeGlobalConfig(
		t, env.galeDir, "[packages]\njq = \""+pin+"\"\n",
	)
	env.jq16 = mkStorePkg(t, env.storeRoot, "jq", "1.6-1")
	env.jq17 = mkStorePkg(t, env.storeRoot, "jq", "1.7-1")
	env.jq18 = mkStorePkg(t, env.storeRoot, "jq", "1.8-1")
	mkActiveGen(t, env.galeDir, 1, filepath.Join(env.jq16, "bin", "jq"))
	mkActiveGen(t, env.galeDir, 2, filepath.Join(env.jq17, "bin", "jq"))
	mkActiveGen(t, env.galeDir, 3, filepath.Join(env.jq18, "bin", "jq"))
	mkActiveGen(t, env.galeDir, current)
	return env
}

// gcBranchEnv holds the fixture's paths: the global gale dir, the
// store root, and the jq store dirs in version order.
type gcBranchEnv struct {
	galeDir   string
	storeRoot string
	jq16      string
	jq17      string
	jq18      string
}

// assertGenerationWhole checks that a generation directory
// survived gc AND that the bin entry it links still resolves.
// The second half is the point: a generation directory gc keeps
// but whose store closure it swept is a hollow generation —
// `gale generations rollback` onto it puts a dangling entry on
// PATH (gh#247).
func assertGenerationWhole(t *testing.T, genDir, why string) {
	t.Helper()
	n := filepath.Base(genDir)
	if _, err := os.Stat(genDir); err != nil {
		t.Errorf("gen/%s %s and must survive gc: %v", n, why, err)
		return
	}
	if _, err := os.Stat(filepath.Join(genDir, "bin", "jq")); err != nil {
		t.Errorf("gen/%s/bin/jq must still resolve after gc — a "+
			"generation gc retains but cannot honour is a hollow "+
			"generation: %v", n, err)
	}
}

// TestGCRetainsGenerationsAboveCurrent is gh#247. After a
// rollback, the generations above current are retained history a
// roll-forward may return to (gh#189), and cleanOldGenerations'
// `n >= curGen` skip already kept their directories. Their store
// closures were swept anyway: retention read only the ACTIVE
// generation, so jq/1.8-1 — linked by gen/2 alone — was
// unreferenced.
//
// Roll back, gc, roll forward, and gen/2 activated with a
// dangling bin/jq on PATH, with no error at any step.
//
// fd@9.0-1 is the control: referenced by nothing, so its removal
// proves the sweep ran rather than that gc did nothing.
func TestGCRetainsGenerationsAboveCurrent(t *testing.T) {
	env := gcBranchFixture(t, 1, "1.7")
	fd := mkStorePkg(t, env.storeRoot, "fd", "9.0-1")

	if err := gcCmd.RunE(gcCmd, nil); err != nil {
		t.Fatalf("gc failed: %v", err)
	}

	if _, err := os.Stat(env.jq18); !os.IsNotExist(err) {
		t.Errorf("jq/1.8-1 is linked only by abandoned gen/2 and must be swept, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(env.galeDir, "gen", "2")); !os.IsNotExist(err) {
		t.Errorf("abandoned gen/2 above current must be swept, err=%v", err)
	}

	if _, err := os.Stat(fd); !os.IsNotExist(err) {
		t.Errorf("fd/9.0-1 is referenced by nothing and must be "+
			"removed (proves the sweep ran), err=%v", err)
	}
}

// TestGCStillReclaimsBelowCurrent is the guard on the other side,
// and gc's most common job (gh#137): once a generation falls
// below the keep-2 cutoff, its directory goes and so does the
// closure only it referenced. Retention covers current + one
// previous and the branch ABOVE current, not history below the
// window.
func TestGCStillReclaimsBelowCurrent(t *testing.T) {
	env := gcThreeGenFixture(t, 3, "1.8")

	if err := gcCmd.RunE(gcCmd, nil); err != nil {
		t.Fatalf("gc failed: %v", err)
	}

	if _, err := os.Stat(
		filepath.Join(env.galeDir, "gen", "1"),
	); !os.IsNotExist(err) {
		t.Errorf("gen/1 is below the keep-2 cutoff and must be "+
			"removed, err=%v", err)
	}
	if _, err := os.Stat(env.jq16); !os.IsNotExist(err) {
		t.Errorf("jq/1.6-1 is referenced only by the removed gen/1 "+
			"and must be swept — retaining it would break `gale "+
			"update && gale gc`, err=%v", err)
	}
	assertGenerationWhole(
		t, filepath.Join(env.galeDir, "gen", "2"), "is the previous generation",
	)
	assertGenerationWhole(
		t, filepath.Join(env.galeDir, "gen", "3"), "is the current generation",
	)
}

// TestGCRetainsProjectBranchAboveCurrent runs the same rule at
// project scope. Scope bugs in sync/gc/remove recur (ad4e685,
// 289d13b), so every retention change is exercised in all three.
func TestGCRetainsProjectBranchAboveCurrent(t *testing.T) {
	_, storeRoot := setupGCHome(t)
	jq17 := mkStorePkg(t, storeRoot, "jq", "1.7-1")
	jq18 := mkStorePkg(t, storeRoot, "jq", "1.8-1")

	proj := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(proj, "gale.toml"),
		[]byte("[packages]\njq = \"1.7\"\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	projGale := filepath.Join(proj, ".gale")
	mkActiveGen(t, projGale, 1, filepath.Join(jq17, "bin", "jq"))
	mkActiveGen(t, projGale, 2, filepath.Join(jq18, "bin", "jq"))
	mkActiveGen(t, projGale, 1) // rolled back to gen/1
	if err := os.WriteFile(
		filepath.Join(os.Getenv("HOME"), ".gale", "projects"),
		[]byte(proj+"\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	t.Chdir(proj)

	if err := gcCmd.RunE(gcCmd, nil); err != nil {
		t.Fatalf("gc failed: %v", err)
	}

	if _, err := os.Stat(jq18); !os.IsNotExist(err) {
		t.Errorf("project abandoned gen/2 target must be swept, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(projGale, "gen", "2")); !os.IsNotExist(err) {
		t.Errorf("abandoned project gen/2 must be swept, err=%v", err)
	}
}

// TestGCProjectKeepsPreviousGeneration exercises the keep-2
// window at project scope (CLAUDE.md: gc changes hit every
// scope). current=3; gen/2 and its exclusive jq@1.7 survive;
// gen/1 and jq@1.6 are swept.
func TestGCProjectKeepsPreviousGeneration(t *testing.T) {
	_, storeRoot := setupGCHome(t)
	jq16 := mkStorePkg(t, storeRoot, "jq", "1.6-1")
	jq17 := mkStorePkg(t, storeRoot, "jq", "1.7-1")
	jq18 := mkStorePkg(t, storeRoot, "jq", "1.8-1")

	proj := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(proj, "gale.toml"),
		[]byte("[packages]\njq = \"1.8\"\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	projGale := filepath.Join(proj, ".gale")
	mkActiveGen(t, projGale, 1, filepath.Join(jq16, "bin", "jq"))
	mkActiveGen(t, projGale, 2, filepath.Join(jq17, "bin", "jq"))
	mkActiveGen(t, projGale, 3, filepath.Join(jq18, "bin", "jq"))
	if err := os.WriteFile(
		filepath.Join(os.Getenv("HOME"), ".gale", "projects"),
		[]byte(proj+"\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	t.Chdir(proj)

	if err := gcCmd.RunE(gcCmd, nil); err != nil {
		t.Fatalf("gc failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(projGale, "gen", "1")); !os.IsNotExist(err) {
		t.Errorf("project gen/1 is below the keep-2 cutoff and must be removed, err=%v", err)
	}
	if _, err := os.Stat(jq16); !os.IsNotExist(err) {
		t.Errorf("jq/1.6-1 is referenced only by the removed project gen/1 and must be swept, err=%v", err)
	}
	assertGenerationWhole(
		t, filepath.Join(projGale, "gen", "2"), "is the project's previous generation",
	)
	if _, err := os.Stat(jq17); err != nil {
		t.Errorf("jq/1.7-1 is linked by the project's previous gen/2 and must survive: %v", err)
	}
}

// TestGCRegisteredProjectKeepsPreviousGeneration is the same
// window from a neutral cwd, via ~/.gale/projects.
func TestGCRegisteredProjectKeepsPreviousGeneration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GALE_OFFLINE", "1")
	t.Chdir(t.TempDir())
	dryRun = false
	t.Cleanup(func() { dryRun = false })

	storeRoot := filepath.Join(home, ".gale", "pkg")
	jq16 := mkStorePkg(t, storeRoot, "jq", "1.6-1")
	jq17 := mkStorePkg(t, storeRoot, "jq", "1.7-1")
	jq18 := mkStorePkg(t, storeRoot, "jq", "1.8-1")
	fd := mkStorePkg(t, storeRoot, "fd", "9.0-1")

	proj := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(proj, "gale.toml"),
		[]byte("[packages]\njq = \"1.8\"\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	projGale := filepath.Join(proj, ".gale")
	mkActiveGen(t, projGale, 1, filepath.Join(jq16, "bin", "jq"))
	mkActiveGen(t, projGale, 2, filepath.Join(jq17, "bin", "jq"))
	mkActiveGen(t, projGale, 3, filepath.Join(jq18, "bin", "jq"))

	if err := os.WriteFile(
		filepath.Join(home, ".gale", "projects"),
		[]byte(proj+"\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	if err := gcCmd.RunE(gcCmd, nil); err != nil {
		t.Fatalf("gc failed: %v", err)
	}

	if _, err := os.Stat(jq17); err != nil {
		t.Errorf("jq/1.7-1 is linked by a REGISTERED project's previous gen/2 and must survive: %v", err)
	}
	assertGenerationWhole(
		t, filepath.Join(projGale, "gen", "2"), "is the registered project's previous generation",
	)
	if _, err := os.Stat(jq16); !os.IsNotExist(err) {
		t.Errorf("jq/1.6-1 is referenced only by the removed registered gen/1 and must be swept, err=%v", err)
	}
	if _, err := os.Stat(fd); !os.IsNotExist(err) {
		t.Errorf("fd/9.0-1 is unreferenced and must be removed (proves the sweep ran), err=%v", err)
	}
}

// TestGCRetainsRegisteredProjectBranchAboveCurrent covers the
// third scope and the ruling that every REGISTERED project
// contributes its whole branch, not only its active generation —
// otherwise a gc run from $HOME reopens the rollback-after-gc
// hole for every project but the cwd's (gh#115 shape, gh#247
// content).
//
// Shape follows TestGCRealRunPreservesRegisteredProjectStoreDirs:
// a real (non-dry) run from a neutral cwd, with fd@9.0-1 as the
// unreferenced control proving the sweep ran.
func TestGCRetainsRegisteredProjectBranchAboveCurrent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GALE_OFFLINE", "1")
	t.Chdir(t.TempDir()) // neutral cwd: no project here
	dryRun = false
	t.Cleanup(func() { dryRun = false })

	storeRoot := filepath.Join(home, ".gale", "pkg")
	jq17 := mkStorePkg(t, storeRoot, "jq", "1.7-1")
	jq18 := mkStorePkg(t, storeRoot, "jq", "1.8-1")
	fd := mkStorePkg(t, storeRoot, "fd", "9.0-1")

	proj := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(proj, "gale.toml"),
		[]byte("[packages]\njq = \"1.7\"\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	projGale := filepath.Join(proj, ".gale")
	mkActiveGen(t, projGale, 1, filepath.Join(jq17, "bin", "jq"))
	mkActiveGen(t, projGale, 2, filepath.Join(jq18, "bin", "jq"))
	mkActiveGen(t, projGale, 1) // rolled back to gen/1

	if err := os.WriteFile(
		filepath.Join(home, ".gale", "projects"),
		[]byte(proj+"\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	if err := gcCmd.RunE(gcCmd, nil); err != nil {
		t.Fatalf("gc failed: %v", err)
	}

	if _, err := os.Stat(jq18); !os.IsNotExist(err) {
		t.Errorf("registered project's abandoned gen/2 target must be swept, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(projGale, "gen", "2")); !os.IsNotExist(err) {
		t.Errorf("abandoned registered gen/2 must be swept, err=%v", err)
	}
	if _, err := os.Stat(fd); !os.IsNotExist(err) {
		t.Errorf("fd/9.0-1 is unreferenced and must be removed "+
			"(proves the sweep ran), err=%v", err)
	}
}

// TestGCRetainsHostOverlayPinsWithBranchAboveCurrent adds the
// host axis. Another host's [hosts.*.packages] overlay pins a
// version this machine hides (gh#48), while a rollback branch
// holds a version only a generation references. Neither source
// alone is the point — the union must survive one sweep.
func TestGCRetainsHostOverlayPinsWithBranchAboveCurrent(t *testing.T) {
	galeDir, storeRoot := setupGCHome(t)
	other := currentHost(t) + "-other"
	writeGlobalConfig(t, galeDir, fmt.Sprintf(
		"[packages]\njq = \"1.7\"\n\n[hosts.%s.packages]\nfd = \"9.0\"\n",
		other,
	))
	jq17 := mkStorePkg(t, storeRoot, "jq", "1.7-1")
	jq18 := mkStorePkg(t, storeRoot, "jq", "1.8-1")
	fd := mkStorePkg(t, storeRoot, "fd", "9.0-1")
	mkActiveGen(t, galeDir, 1, filepath.Join(jq17, "bin", "jq"))
	mkActiveGen(t, galeDir, 2, filepath.Join(jq18, "bin", "jq"))
	mkActiveGen(t, galeDir, 1) // rolled back to gen/1

	if err := gcCmd.RunE(gcCmd, nil); err != nil {
		t.Fatalf("gc failed: %v", err)
	}

	if _, err := os.Stat(fd); !os.IsNotExist(err) {
		t.Errorf("host overlay pin with no kept-gen link must be swept, err=%v", err)
	}
	if _, err := os.Stat(jq18); !os.IsNotExist(err) {
		t.Errorf("abandoned gen/2 target must be swept, err=%v", err)
	}
}

// TestRollbackRefusesIncompleteGeneration is gh#247's second
// half. `build` refuses to activate a generation with a dangling
// symlink (validateGenerationSymlinks, called between populate
// and swap); Rollback is the same activation and had no
// equivalent. It stat'd the directory, read the shrunken package
// set through the LENIENT reader — which skips a package whose
// store dir is gone, by contract — staged the farm from that,
// and swapped.
//
// The result was a successful-looking rollback onto dangling
// PATH entries. Repair is not an option here: it would invert
// the generation → installer package order, and carrying a
// different version forward would violate the one-number-
// one-snapshot invariant (gh#189).
//
// Independent of the retention rule: an incomplete generation
// can also come from a crash, a manual store edit, or a gc run
// under an older gale.
func TestRollbackRefusesIncompleteGeneration(t *testing.T) {
	env := gcBranchFixture(t, 1, "1.7")
	// gen/2's only package leaves the store.
	if err := os.RemoveAll(env.jq18); err != nil {
		t.Fatal(err)
	}

	generationsGlobal = true
	dryRun = false
	t.Cleanup(func() {
		generationsGlobal = false
		dryRun = false
	})

	err := genRollbackCmd.RunE(genRollbackCmd, []string{"2"})
	if err == nil {
		t.Fatal("rollback onto a generation whose store closure " +
			"is incomplete must refuse: gen/2/bin/jq dangles, and " +
			"activating it puts a broken entry on PATH")
	}
	if !strings.Contains(err.Error(), "2") {
		t.Errorf("refusal must name the generation, got: %v", err)
	}

	cur, cErr := generation.Current(env.galeDir)
	if cErr != nil {
		t.Fatalf("reading current after refused rollback: %v", cErr)
	}
	if cur != 1 {
		t.Errorf("current = gen/%d after a refused rollback, want "+
			"gen/1 — a refusal must leave the active generation "+
			"untouched", cur)
	}
}
