package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/kelp/gale/internal/config"
)

// gcKeepFixture stages the two-generation shape every retention
// test in this file shares: jq@1.7-1 linked by gen/1, jq@1.8-1
// linked by gen/2, `current` pointed at the caller's choice, and
// a global gale.toml pinning exactly one of the two versions —
// so the other survives only through a generation.
//
// Both generations are created WITH their bin targets first, and
// `current` is re-pointed afterwards by a target-less mkActiveGen
// call. Creating the active generation last with its targets
// fails on the bin symlink that already exists (gh#252).
func gcKeepFixture(t *testing.T, current int, pin string) gcKeepEnv {
	t.Helper()
	env := gcKeepEnv{}
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

// gcKeepEnv holds the fixture's paths: the global gale dir, the
// store root, and the two jq store dirs in version order.
type gcKeepEnv struct {
	galeDir   string
	storeRoot string
	jq17      string
	jq18      string
}

// writeGenerationKeep writes ~/.gale/config.toml with an explicit
// [generation] keep. writeGlobalConfig writes gale.toml (the
// package config); loadAppConfig reads config.toml, so the knob
// needs its own file.
func writeGenerationKeep(t *testing.T, galeDir string, keep int) {
	t.Helper()
	if err := os.WriteFile(
		filepath.Join(galeDir, "config.toml"),
		[]byte(fmt.Sprintf("[generation]\nkeep = %d\n", keep)),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
}

// assertGenerationWhole checks that a generation directory
// survived gc AND that the bin entry it links still resolves.
// The second half is the point: a generation directory gc keeps
// but whose store closure it swept is a hollow generation —
// `gale generations rollback` onto it puts a dangling entry on
// PATH (gh#247).
func assertGenerationWhole(t *testing.T, galeDir string, n int, why string) {
	t.Helper()
	genDir := filepath.Join(galeDir, "gen", fmt.Sprint(n))
	if _, err := os.Stat(genDir); err != nil {
		t.Errorf("gen/%d %s and must survive gc: %v", n, why, err)
		return
	}
	if _, err := os.Stat(filepath.Join(genDir, "bin", "jq")); err != nil {
		t.Errorf("gen/%d/bin/jq must still resolve after gc — a "+
			"generation gc retains but cannot honour is a hollow "+
			"generation: %v", n, err)
	}
}

// TestGCRetainsGenerationsWithinKeep pins the first half of
// gh#247 at or below current: gc keeps `[generation] keep`
// generations, so it must keep their store closures too. Here
// gen/1 links jq@1.7-1 and nothing else does — the config pins
// 1.8 and current is gen/2.
//
// Pre-fix gc retained only the ACTIVE generation's links, so
// jq/1.7-1 was swept, and cleanOldGenerations removed gen/1
// outright because its rule was "older than current".
func TestGCRetainsGenerationsWithinKeep(t *testing.T) {
	env := gcKeepFixture(t, 2, "1.8")

	if err := gcCmd.RunE(gcCmd, nil); err != nil {
		t.Fatalf("gc failed: %v", err)
	}

	if _, err := os.Stat(env.jq17); err != nil {
		t.Errorf("jq/1.7-1 is linked by gen/1, which is within "+
			"[generation] keep, so its store dir must survive gc: %v",
			err)
	}
	assertGenerationWhole(t, env.galeDir, 1, "is within [generation] keep")
}

// TestGCRetainsGenerationsAboveCurrent is gh#247's core. After a
// rollback, generations above current are retained history a
// user may roll forward into (gh#189), and cleanOldGenerations'
// `n >= curGen` skip already kept their directories. Their store
// closures were swept anyway: retention read only the active
// generation, so jq/1.8-1 — linked by gen/2 alone — was
// unreferenced.
//
// Roll back, gc, roll forward, and gen/2 activated with a
// dangling bin/jq on PATH.
func TestGCRetainsGenerationsAboveCurrent(t *testing.T) {
	env := gcKeepFixture(t, 1, "1.7")

	if err := gcCmd.RunE(gcCmd, nil); err != nil {
		t.Fatalf("gc failed: %v", err)
	}

	if _, err := os.Stat(env.jq18); err != nil {
		t.Errorf("jq/1.8-1 is linked by gen/2, which sits above "+
			"current and is retained unconditionally, so its store "+
			"dir must survive gc: %v", err)
	}
	assertGenerationWhole(t, env.galeDir, 2, "sits above current")
}

// TestGCNegativeKeepRemovesNoGenerationsButStillSweeps pins the
// negative-keep ruling: `keep = -1` disables generation
// retirement entirely — every generation directory and every
// generation's closure is retained — but gc is not a no-op. A
// version no generation and no config references is still swept,
// as are crash leftovers.
func TestGCNegativeKeepRemovesNoGenerationsButStillSweeps(t *testing.T) {
	env := gcKeepFixture(t, 2, "1.8")
	writeGenerationKeep(t, env.galeDir, -1)
	// Referenced by no config and no generation.
	fd := mkStorePkg(t, env.storeRoot, "fd", "9.0-1")

	if err := gcCmd.RunE(gcCmd, nil); err != nil {
		t.Fatalf("gc failed: %v", err)
	}

	for _, dir := range []string{env.jq17, env.jq18} {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("%s is linked by a generation and keep is "+
				"negative, so it must survive gc: %v", dir, err)
		}
	}
	assertGenerationWhole(t, env.galeDir, 1, "is retained by a negative keep")
	assertGenerationWhole(t, env.galeDir, 2, "is the current generation")

	if _, err := os.Stat(fd); !os.IsNotExist(err) {
		t.Errorf("fd/9.0-1 is referenced by nothing — a negative "+
			"keep retains generations, it does not stop the store "+
			"sweep: err=%v", err)
	}
}

// TestGCKeepAppliesToProjectScope runs the same rule at project
// scope. Scope bugs in sync/gc/remove recur (ad4e685, 289d13b),
// so every retention change is exercised in all three.
func TestGCKeepAppliesToProjectScope(t *testing.T) {
	_, storeRoot := setupGCHome(t)
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
	mkActiveGen(t, projGale, 1, filepath.Join(jq17, "bin", "jq"))
	mkActiveGen(t, projGale, 2, filepath.Join(jq18, "bin", "jq"))
	mkActiveGen(t, projGale, 2)
	t.Chdir(proj)

	if err := gcCmd.RunE(gcCmd, nil); err != nil {
		t.Fatalf("gc failed: %v", err)
	}

	if _, err := os.Stat(jq17); err != nil {
		t.Errorf("jq/1.7-1 is linked by the project's gen/1, which "+
			"is within keep, so it must survive gc: %v", err)
	}
	assertGenerationWhole(t, projGale, 1, "is within the project's keep")
}

// TestGCRetainsRegisteredProjectGenerationsWithinKeep covers the
// third scope and the maintainer ruling that every REGISTERED
// project contributes `keep` generations' closures, not only its
// active one — otherwise a gc run from $HOME reopens the
// rollback-after-gc hole for every project but the cwd's
// (gh#115 shape, gh#247 content).
//
// Shape follows TestGCRealRunPreservesRegisteredProjectStoreDirs:
// a real (non-dry) run from a neutral cwd, with fd@9.0-1 as the
// unreferenced control proving the sweep ran.
func TestGCRetainsRegisteredProjectGenerationsWithinKeep(t *testing.T) {
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
		[]byte("[packages]\njq = \"1.8\"\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	projGale := filepath.Join(proj, ".gale")
	mkActiveGen(t, projGale, 1, filepath.Join(jq17, "bin", "jq"))
	mkActiveGen(t, projGale, 2, filepath.Join(jq18, "bin", "jq"))
	mkActiveGen(t, projGale, 2)

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
		t.Errorf("jq/1.7-1 is linked only by a REGISTERED "+
			"project's gen/1, which is within keep, so it must "+
			"survive gc: %v", err)
	}
	assertGenerationWhole(
		t, projGale, 1, "is within the registered project's keep",
	)
	if _, err := os.Stat(fd); !os.IsNotExist(err) {
		t.Errorf("fd/9.0-1 is unreferenced and must be removed "+
			"(proves the sweep ran), err=%v", err)
	}
}

// TestGCRetainsHostOverlayPinsInRetainedGenerations adds the
// host axis to the retention change: another host's
// [hosts.*.packages] overlay pins a version this machine hides
// (gh#48), and that version is also all a retained generation
// links. Neither source alone is the point — the union must
// survive a keep-aware sweep.
func TestGCRetainsHostOverlayPinsInRetainedGenerations(t *testing.T) {
	galeDir, storeRoot := setupGCHome(t)
	other := config.CurrentHost() + "-other"
	writeGlobalConfig(t, galeDir, fmt.Sprintf(
		"[packages]\njq = \"1.8\"\n\n[hosts.%s.packages]\nfd = \"9.0\"\n",
		other,
	))
	jq17 := mkStorePkg(t, storeRoot, "jq", "1.7-1")
	jq18 := mkStorePkg(t, storeRoot, "jq", "1.8-1")
	fd := mkStorePkg(t, storeRoot, "fd", "9.0-1")
	mkActiveGen(t, galeDir, 1, filepath.Join(jq17, "bin", "jq"))
	mkActiveGen(t, galeDir, 2, filepath.Join(jq18, "bin", "jq"))
	mkActiveGen(t, galeDir, 2)

	if err := gcCmd.RunE(gcCmd, nil); err != nil {
		t.Fatalf("gc failed: %v", err)
	}

	if _, err := os.Stat(fd); err != nil {
		t.Errorf("fd/9.0-1 is pinned by another host's overlay and "+
			"must survive gc: %v", err)
	}
	if _, err := os.Stat(jq17); err != nil {
		t.Errorf("jq/1.7-1 is linked by gen/1, within keep, and "+
			"must survive gc: %v", err)
	}
	assertGenerationWhole(t, galeDir, 1, "is within [generation] keep")
}

// TestGCDeletesGenerationsBeyondKeep is the guard on the other
// side: the fix is "gc honours keep", not "gc never deletes".
// With keep = 1 — the only setting that reproduces the old
// current-only behavior, since keep = 0 is unreachable under
// omitempty — gen/1 and its exclusive store closure go.
func TestGCDeletesGenerationsBeyondKeep(t *testing.T) {
	env := gcKeepFixture(t, 2, "1.8")
	writeGenerationKeep(t, env.galeDir, 1)

	if err := gcCmd.RunE(gcCmd, nil); err != nil {
		t.Fatalf("gc failed: %v", err)
	}

	if _, err := os.Stat(
		filepath.Join(env.galeDir, "gen", "1"),
	); !os.IsNotExist(err) {
		t.Errorf("gen/1 is beyond keep = 1 and must be removed, "+
			"err=%v", err)
	}
	if _, err := os.Stat(env.jq17); !os.IsNotExist(err) {
		t.Errorf("jq/1.7-1 is referenced only by the removed "+
			"gen/1 and must be swept, err=%v", err)
	}
}

// TestCleanOldGenerationsNeverRemovesCurrentOrInFlight pins the
// guarantee the old `n >= curGen` skip carried, across every
// keep a user can configure. curGen is retained by rule A for
// any keep >= 1 and by the retain-all branch otherwise; an
// in-flight gen/curGen+1 a concurrent Build created is above
// current, which rule B retains unconditionally.
//
// The property is the whole reason the rewrite could drop the
// explicit skip, so it is asserted directly rather than inferred
// from the retention rule.
func TestCleanOldGenerationsNeverRemovesCurrentOrInFlight(t *testing.T) {
	for _, keep := range []int{-1, 1, 2, 3, 10} {
		t.Run(fmt.Sprintf("keep=%d", keep), func(t *testing.T) {
			galeDir := t.TempDir()
			storeRoot := filepath.Join(galeDir, "pkg")
			if err := os.MkdirAll(storeRoot, 0o755); err != nil {
				t.Fatal(err)
			}
			// gens 1..5, current at 4, so gen/5 stands in for the
			// in-flight gen/curGen+1.
			for i := 1; i <= 5; i++ {
				if err := os.MkdirAll(filepath.Join(
					galeDir, "gen", fmt.Sprint(i), "bin",
				), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(
				filepath.Join("gen", "4"),
				filepath.Join(galeDir, "current"),
			); err != nil {
				t.Fatal(err)
			}

			dryRun = false
			t.Cleanup(func() { dryRun = false })
			cleanOldGenerations(galeDir, storeRoot, keep, false)

			for _, n := range []int{4, 5} {
				if _, err := os.Stat(filepath.Join(
					galeDir, "gen", fmt.Sprint(n),
				)); err != nil {
					t.Errorf("gen/%d must survive keep = %d — it is "+
						"the current generation or an in-flight "+
						"gen/curGen+1: %v", n, keep, err)
				}
			}
		})
	}
}
