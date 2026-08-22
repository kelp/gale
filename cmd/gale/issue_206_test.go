package main

import (
	"bytes"
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
// `n >= curGen` guard and by PruneOldGenerations, which counts
// only the generations at or below current (gh#248),
// and reclaimed only once `current` climbs back past them.
//
// The listing rendered them identically to history below current —
// one marker column, "*" for current and " " for everything else —
// so the state that explains "why is gen/4 still here?" was
// invisible. A user cannot see which gens a later rebuild
// must climb past without the listing distinguishing them.
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
