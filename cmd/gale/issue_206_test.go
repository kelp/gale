package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// genMarkers runs `gale generations` at global scope and returns
// each generation number mapped to the one-column marker the
// listing prints for it.
func genMarkers(t *testing.T) map[int]byte {
	t.Helper()

	var buf bytes.Buffer
	generationsCmd.SetOut(&buf)
	t.Cleanup(func() { generationsCmd.SetOut(nil) })

	if err := generationsCmd.RunE(generationsCmd, nil); err != nil {
		t.Fatalf("gale generations: %v", err)
	}

	markers := make(map[int]byte)
	for _, line := range strings.Split(buf.String(), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line[1:])
		if len(fields) == 0 {
			t.Fatalf("unparsable listing line %q", line)
		}
		n, err := strconv.Atoi(fields[0])
		if err != nil {
			t.Fatalf("unparsable generation number in %q: %v", line, err)
		}
		markers[n] = line[0]
	}
	return markers
}

// TestGenerationsListMarksBranchAboveCurrent pins the visibility
// half of gh#206. After a rollback, `current` points below the
// highest generation and the gens above it are an abandoned
// branch: reachable only by rolling forward, skipped by gc's
// `n >= curGen` guard and by PruneOldGenerations' numeric cutoff,
// and reclaimed only once `current` climbs back past them.
//
// The listing rendered them identically to history below current —
// one marker column, "*" for current and " " for everything else —
// so the state that explains "why is gen/4 still here?" was
// invisible. A user cannot decide what to pass to `gale
// generations remove` without seeing which gens are the branch.
func TestGenerationsListMarksBranchAboveCurrent(t *testing.T) {
	setupRollbackBranch(t)

	markers := genMarkers(t)
	if len(markers) != 4 {
		t.Fatalf("listing showed %d generations, want 4", len(markers))
	}

	if markers[2] != '*' {
		t.Errorf("gen 2 marker = %q, want %q (the active generation)",
			string(markers[2]), "*")
	}
	for _, n := range []int{3, 4} {
		if markers[n] == markers[1] {
			t.Errorf("gen %d marker = %q, same as gen 1's — a "+
				"generation above current is retained history a "+
				"rollback abandoned, not history below current, "+
				"and the listing must distinguish the two",
				n, string(markers[n]))
		}
		if markers[n] == '*' {
			t.Errorf("gen %d marker = %q, want a marker distinct "+
				"from the active generation's", n, string(markers[n]))
		}
	}
}

// setupRollbackBranch stages gens 1..4 linking one store package
// and rolls current back to gen/2, leaving gens 3 and 4 as the
// abandoned branch gh#206 is about. Returns the global gale dir.
func setupRollbackBranch(t *testing.T) string {
	t.Helper()
	galeDir, storeRoot := setupGCHome(t)
	jq := mkStorePkg(t, storeRoot, "jq", "1.7-1") + "/bin/jq"
	for n := 1; n <= 4; n++ {
		mkActiveGen(t, galeDir, n, jq)
	}
	// No bin targets: gen/2 already holds its link from the loop
	// above, and mkActiveGen re-points current either way.
	mkActiveGen(t, galeDir, 2)
	generationsGlobal = true
	t.Cleanup(func() { generationsGlobal = false })
	return galeDir
}

// genDirExists reports whether gen/<n> is present under galeDir.
func genDirExists(t *testing.T, galeDir string, n int) bool {
	t.Helper()
	_, err := os.Lstat(filepath.Join(galeDir, "gen", strconv.Itoa(n)))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("stat gen/%d: %v", n, err)
	}
	return err == nil
}

// TestGenerationsRemoveDiscardsNamedGeneration pins the verb half
// of gh#206: a user who rolled back on purpose can reclaim the
// abandoned branch by naming it, without waiting for current to
// climb back past the retention cutoff.
func TestGenerationsRemoveDiscardsNamedGeneration(t *testing.T) {
	galeDir := setupRollbackBranch(t)

	if err := genRemoveCmd.RunE(genRemoveCmd, []string{"4"}); err != nil {
		t.Fatalf("gale generations remove 4: %v", err)
	}

	if genDirExists(t, galeDir, 4) {
		t.Error("gen/4 must be gone after `generations remove 4`")
	}
	for _, n := range []int{1, 2, 3} {
		if !genDirExists(t, galeDir, n) {
			t.Errorf("gen/%d must survive `generations remove 4` — "+
				"only the named generation is removed", n)
		}
	}
	target, err := os.Readlink(filepath.Join(galeDir, "current"))
	if err != nil {
		t.Fatalf("read current symlink: %v", err)
	}
	if filepath.Base(target) != "2" {
		t.Errorf("current = gen/%s, want gen/2 — removing another "+
			"generation must not move the active one",
			filepath.Base(target))
	}
}

// TestGenerationsRemoveRefusesCurrent pins the guard that keeps
// current from dangling: removing the active generation would
// empty PATH and turn doctor's generation check red. The refusal
// is at the internal layer; this asserts the command surfaces it
// and leaves the directory alone.
func TestGenerationsRemoveRefusesCurrent(t *testing.T) {
	galeDir := setupRollbackBranch(t)

	err := genRemoveCmd.RunE(genRemoveCmd, []string{"2"})
	if err == nil {
		t.Fatal("`generations remove 2` removed the active " +
			"generation, want a refusal")
	}
	if !strings.Contains(err.Error(), "2") {
		t.Errorf("error = %q, want it to name generation 2", err)
	}
	if !genDirExists(t, galeDir, 2) {
		t.Error("gen/2 is current and must survive a refused remove")
	}
}

// TestGenerationsRemoveRejectsNonDirectory pins that only a
// numeric *directory* is a generation. A stray regular file at
// gen/5 is not one, so `remove 5` reports it does not exist
// rather than deleting whatever sits there — genNumbers skips
// non-directories (history.go), and Remove validates against
// that scan instead of stat'ing the path itself.
func TestGenerationsRemoveRejectsNonDirectory(t *testing.T) {
	galeDir := setupRollbackBranch(t)
	stray := filepath.Join(galeDir, "gen", "5")
	if err := os.WriteFile(stray, []byte("not a generation"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := genRemoveCmd.RunE(genRemoveCmd, []string{"5"})
	if err == nil {
		t.Fatal("`generations remove 5` accepted a regular file " +
			"at gen/5, want a refusal")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("error = %q, want it to report generation 5 "+
			"does not exist", err)
	}
	data, readErr := os.ReadFile(stray)
	if readErr != nil {
		t.Fatalf("the stray file must be left alone: %v", readErr)
	}
	if string(data) != "not a generation" {
		t.Errorf("stray file content = %q, want it untouched", data)
	}
}

// TestGenerationsRemoveValidatesAllTargetsFirst pins the
// all-or-nothing contract: `remove 3 2` with current at 2 must
// remove nothing. Removing gen/3 and then refusing gen/2 would
// destroy a snapshot the user never got told about, in a command
// they will read as having failed.
func TestGenerationsRemoveValidatesAllTargetsFirst(t *testing.T) {
	galeDir := setupRollbackBranch(t)

	if err := genRemoveCmd.RunE(genRemoveCmd, []string{"3", "2"}); err == nil {
		t.Fatal("`generations remove 3 2` with current at 2 " +
			"succeeded, want a refusal")
	}

	for n := 1; n <= 4; n++ {
		if !genDirExists(t, galeDir, n) {
			t.Errorf("gen/%d must survive a batch that names the "+
				"active generation — every target is validated "+
				"before the first removal", n)
		}
	}
}
