package installer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/store"
)

// TestCommitStagedRefusesOccupiedWithoutGuard checks that a
// staged reinstall cannot rename over an occupied store dir
// when ReplaceGuard is unset (gh#211).
func TestCommitStagedRefusesOccupiedWithoutGuard(t *testing.T) {
	root := t.TempDir()
	storeRoot := filepath.Join(root, "pkg")
	canonical := filepath.Join(storeRoot, "jq", "1.7-1")
	staging := filepath.Join(storeRoot, "jq", ".build-x")
	if err := os.MkdirAll(canonical, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(canonical, "bin", "jq")
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "new"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := (&Installer{}).commitStaged(storeRoot, staging, Replacement{
		CanonicalDir: canonical,
		StagingDir:   staging,
	})
	if err == nil {
		t.Fatal("commitStaged replaced an occupied dest with no ReplaceGuard")
	}
	if !errors.Is(err, ErrReplaceUnwired) {
		t.Errorf("error = %v, want ErrReplaceUnwired", err)
	}
	got, readErr := os.ReadFile(marker)
	if readErr != nil || string(got) != "old\n" {
		t.Errorf("dest mutated: %q, %v", got, readErr)
	}
	if _, statErr := os.Stat(canonical + ".bak"); statErr == nil {
		t.Error("replace left a .bak sibling")
	}
}

// TestReinstallDoesNotReplaceOccupiedStoreDir checks that
// Reinstall leaves an occupied dest unchanged (gh#211).
func TestReinstallDoesNotReplaceOccupiedStoreDir(t *testing.T) {
	storeRoot := t.TempDir()
	inst := &Installer{Store: store.NewStore(storeRoot)}
	dir, err := inst.Store.Create("jq", "1.7-1")
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "bin", "jq")
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := inst.Reinstall(context.Background(), &recipe.Recipe{
		Package: recipe.Package{Name: "jq", Version: "1.7"},
	})
	if got != nil {
		t.Errorf("result = %+v, want nil", got)
	}
	if !errors.Is(err, ErrBottleGone) {
		t.Errorf("error = %v, want ErrBottleGone", err)
	}
	body, readErr := os.ReadFile(marker)
	if readErr != nil || string(body) != "old\n" {
		t.Errorf("dest mutated: %q, %v", body, readErr)
	}
}
