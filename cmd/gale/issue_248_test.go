package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/generation"
)

// TestGCDoesNotUndoPositionalAutoPrune checks that gale gc
// keeps gen/5 after auto-gc prunes generations 1, 5, 10
// with current at 10.
func TestGCDoesNotUndoPositionalAutoPrune(t *testing.T) {
	galeDir, storeRoot := setupGCHome(t)
	writeGlobalConfig(t, galeDir, "[packages]\njq = \"1.8\"\n")
	jq16 := mkStorePkg(t, storeRoot, "jq", "1.6-1")
	jq17 := mkStorePkg(t, storeRoot, "jq", "1.7-1")
	jq18 := mkStorePkg(t, storeRoot, "jq", "1.8-1")
	mkActiveGen(t, galeDir, 1, filepath.Join(jq16, "bin", "jq"))
	mkActiveGen(t, galeDir, 5, filepath.Join(jq17, "bin", "jq"))
	mkActiveGen(t, galeDir, 10, filepath.Join(jq18, "bin", "jq"))

	removed, err := generation.PruneOldGenerations(
		galeDir, storeRoot, config.DefaultGenerationKeep,
	)
	if err != nil {
		t.Fatalf("PruneOldGenerations: %v", err)
	}
	if len(removed) != 1 || removed[0] != 1 {
		t.Fatalf("auto-gc removed %v, want [1] — keep-2 of "+
			"1, 5, 10 is 5 and 10", removed)
	}

	if err := gcCmd.RunE(gcCmd, nil); err != nil {
		t.Fatalf("gc failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(galeDir, "gen", "5")); err != nil {
		t.Errorf("gen/5 is the previous existing generation and "+
			"must survive gale gc: %v", err)
	}
	if _, err := os.Stat(jq17); err != nil {
		t.Errorf("jq/1.7-1 is linked only by gen/5 and must "+
			"survive gale gc: %v", err)
	}
	if _, err := os.Stat(filepath.Join(galeDir, "gen", "1")); !os.IsNotExist(err) {
		t.Errorf("gen/1 is below keep-2 and must be gone after "+
			"auto-gc, err=%v", err)
	}
}
