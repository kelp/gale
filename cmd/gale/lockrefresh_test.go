package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/provenance"
)

func TestLockRefreshFlagGone(t *testing.T) {
	if lockCmd.Flags().Lookup("refresh") != nil {
		t.Fatal("gale lock: --refresh must be deleted")
	}
}

func TestLockRefusesPackageArgs(t *testing.T) {
	err := checkLockArgs([]string{"jq"})
	if !errors.Is(err, errLockTakesNoPackages) {
		t.Errorf("err = %v, want errLockTakesNoPackages", err)
	}
	if !strings.Contains(err.Error(), "fetch-adopt") {
		t.Errorf("refusal must name fetch-adopt: %v", err)
	}
	if err := checkLockArgs(nil); err != nil {
		t.Errorf("plain `gale lock` refused: %v", err)
	}
}

func TestLockDoesNotReplaceUnprovenanced(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	ctx := lockCtx(t, tmp, "[packages]\n  hello = \"1.0.0\"\n",
		map[string]string{"hello": "1.0.0"})
	dir := seedStore(t, ctx.StoreRoot, "hello", "1.0.0-1")
	marker := filepath.Join(dir, "legacy-marker")
	if err := os.WriteFile(marker, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := runLock(ctx, "", discardOutput())
	if !errors.Is(err, errUnprovenancedStoreDir) {
		t.Fatalf("err = %v, want errUnprovenancedStoreDir", err)
	}
	if strings.Contains(err.Error(), "--refresh") {
		t.Errorf("dead --refresh named as a remedy: %v", err)
	}
	if !strings.Contains(err.Error(), "fetch-adopt") {
		t.Errorf("unprovenanced remedy must name fetch-adopt: %v", err)
	}
	kept, rerr := os.ReadFile(marker)
	if rerr != nil {
		t.Fatalf("store dir was replaced: %v", rerr)
	}
	if string(kept) != "old" {
		t.Errorf("marker holds %q, want old", kept)
	}
	if _, statErr := os.Lstat(filepath.Join(dir, provenance.File)); !os.IsNotExist(statErr) {
		t.Errorf("lock stamped provenance onto unverified bytes: %v", statErr)
	}
}
