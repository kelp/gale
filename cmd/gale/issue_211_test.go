package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/recipe"
)

// TestUnlockedStaleSyncDoesNotReplaceOccupiedStoreDir checks
// that unlocked sync's stale Reinstall leaves occupied store
// bytes unchanged (gh#211).
func TestUnlockedStaleSyncDoesNotReplaceOccupiedStoreDir(t *testing.T) {
	tmp := t.TempDir()
	storeRoot := filepath.Join(tmp, "store")
	galeDir := filepath.Join(tmp, ".gale")
	dir := seedStore(t, storeRoot, "jq", "1.7-1")
	marker := filepath.Join(dir, "bin", "jq")
	if err := os.WriteFile(marker, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cc := buildFakeCtx(t,
		filepath.Join(tmp, "gale.toml"), galeDir, storeRoot,
		func(_ context.Context, _ string) (*recipe.Recipe, error) {
			return minimalRecipe("jq", "1.7"), nil
		})

	out := runSyncOne(context.Background(), cc, syncItem{
		name: "jq", version: "1.7",
	}, false)
	if out.installErr == nil {
		t.Fatal("stale sync replaced an occupied dest")
	}
	if !errors.Is(out.installErr, installer.ErrBottleGone) {
		t.Errorf("installErr = %v, want ErrBottleGone", out.installErr)
	}
	got, err := os.ReadFile(marker)
	if err != nil || string(got) != "old\n" {
		t.Errorf("dest mutated: %q, %v", got, err)
	}
	if _, err := os.Stat(dir + ".bak"); err == nil {
		t.Error("replace left a .bak sibling")
	}
}
