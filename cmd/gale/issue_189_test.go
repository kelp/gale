package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/kelp/gale/internal/generation"
)

// TestSyncAfterRollbackAllocatesAboveHighestGeneration pins
// gh#189 at the pipeline layer: a generation number, once
// allocated, permanently identifies one snapshot. `current` is a
// pointer into history, not a high-water mark.
//
// Pre-fix, generation.Build took Current()+1 as the next number.
// After `gale generations rollback 5`, the next rebuild landed on
// gen/6 and os.RemoveAll destroyed the snapshot that number named;
// further rebuilds ate 7, 8, ... Generation numbers became
// branch-relative, so a later `rollback 7` either failed or, worse,
// activated a directory holding different content.
//
// Every mutating command (install, update, sync, add, remove)
// reaches Build through rebuildGeneration, so driving that helper
// plus the real rollback command covers the whole surface.
func TestSyncAfterRollbackAllocatesAboveHighestGeneration(t *testing.T) {
	galeDir, storeRoot := setupGCHome(t)
	configPath := filepath.Join(galeDir, "gale.toml")

	// Two builds, each pinning a distinct jq version, so the
	// abandoned generation's bin/jq resolves to a different
	// store dir. A clobbered gen is then detectable by its
	// symlink target, not merely by its absence. keep=2 would
	// prune a longer fixture before the rollback can run.
	const gens = 2
	for i := 1; i <= gens; i++ {
		version := fmt.Sprintf("1.%d", i)
		mkStorePkg(t, storeRoot, "jq", version+"-1")
		writeGlobalConfig(t, galeDir,
			"[packages]\njq = \""+version+"\"\n")
		if err := rebuildGeneration(
			galeDir, storeRoot, configPath, nil,
		); err != nil {
			t.Fatalf("rebuildGeneration %d: %v", i, err)
		}
	}
	if cur, err := generation.Current(galeDir); err != nil || cur != gens {
		t.Fatalf("setup: current = %d (err=%v), want %d", cur, err, gens)
	}

	want2 := readGenJQ(t, galeDir, gens)

	if err := genRollbackCmd.RunE(genRollbackCmd, []string{"1"}); err != nil {
		t.Fatalf("rollback to gen 1: %v", err)
	}

	// The sync that follows the rollback.
	if err := rebuildGeneration(
		galeDir, storeRoot, configPath, nil,
	); err != nil {
		t.Fatalf("rebuildGeneration after rollback: %v", err)
	}

	cur, err := generation.Current(galeDir)
	if err != nil {
		t.Fatalf("read current: %v", err)
	}
	if cur != gens+1 {
		t.Errorf("current generation = %d, want %d — a rebuild "+
			"after rollback must allocate above the highest "+
			"generation, never reuse a number", cur, gens+1)
	}

	got2 := readGenJQ(t, galeDir, gens)
	if got2 != want2 {
		t.Errorf("gen/%d/bin/jq = %q, want %q — the rebuild "+
			"overwrote a generation the rollback left intact",
			gens, got2, want2)
	}

	// Retention still bounds growth: with keep=2 and current at
	// 3, the cutoff is 2, so gen/1 goes.
	if _, err := os.Stat(
		filepath.Join(galeDir, "gen", "1"),
	); !os.IsNotExist(err) {
		t.Errorf("gen/1 must be auto-pruned once current reaches "+
			"%d (err=%v)", gens+1, err)
	}
}

// readGenJQ returns the symlink target of gen/<n>/bin/jq.
func readGenJQ(t *testing.T, galeDir string, n int) string {
	t.Helper()
	target, err := os.Readlink(filepath.Join(
		galeDir, "gen", strconv.Itoa(n), "bin", "jq",
	))
	if err != nil {
		t.Fatalf("read gen/%d/bin/jq: %v", n, err)
	}
	return target
}
