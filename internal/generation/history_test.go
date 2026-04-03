package generation

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListReturnsAllGenerations(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	createStoreEntry(t, storeRoot, "jq", "1.7.1", []string{"jq"})
	createStoreEntry(t, storeRoot, "fd", "9.0", []string{"fd"})
	createStoreEntry(t, storeRoot, "rg", "14.0", []string{"rg"})

	// Gen 1: jq only.
	if err := Build(map[string]string{"jq": "1.7.1"}, galeDir, storeRoot); err != nil {
		t.Fatalf("Build gen 1: %v", err)
	}
	// Gen 2: jq + fd.
	if err := Build(map[string]string{"jq": "1.7.1", "fd": "9.0"}, galeDir, storeRoot); err != nil {
		t.Fatalf("Build gen 2: %v", err)
	}
	// Gen 3: jq + fd + rg.
	if err := Build(map[string]string{"jq": "1.7.1", "fd": "9.0", "rg": "14.0"}, galeDir, storeRoot); err != nil {
		t.Fatalf("Build gen 3: %v", err)
	}

	gens, err := List(galeDir, storeRoot)
	if err != nil {
		t.Fatalf("List error: %v", err)
	}

	if len(gens) != 3 {
		t.Fatalf("expected 3 generations, got %d", len(gens))
	}

	// Verify sorted by number ascending.
	for i, want := range []int{1, 2, 3} {
		if gens[i].Number != want {
			t.Errorf("gens[%d].Number = %d, want %d", i, gens[i].Number, want)
		}
	}

	// Current should be gen 3.
	for i, g := range gens {
		if g.Current != (g.Number == 3) {
			t.Errorf("gens[%d].Current = %v, want %v", i, g.Current, g.Number == 3)
		}
	}

	// Verify package counts.
	if len(gens[0].Packages) != 1 {
		t.Errorf("gen 1 packages = %d, want 1", len(gens[0].Packages))
	}
	if len(gens[1].Packages) != 2 {
		t.Errorf("gen 2 packages = %d, want 2", len(gens[1].Packages))
	}
	if len(gens[2].Packages) != 3 {
		t.Errorf("gen 3 packages = %d, want 3", len(gens[2].Packages))
	}

	// Verify specific packages.
	if v, ok := gens[0].Packages["jq"]; !ok || v != "1.7.1" {
		t.Errorf("gen 1 jq = %q, want 1.7.1", v)
	}
	if v, ok := gens[1].Packages["fd"]; !ok || v != "9.0" {
		t.Errorf("gen 2 fd = %q, want 9.0", v)
	}
}

func TestListSkipsNonNumericDirs(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	createStoreEntry(t, storeRoot, "jq", "1.7.1", []string{"jq"})

	if err := Build(map[string]string{"jq": "1.7.1"}, galeDir, storeRoot); err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Create a non-numeric directory in gen/.
	if err := os.MkdirAll(filepath.Join(galeDir, "gen", "garbage"), 0o755); err != nil {
		t.Fatalf("create garbage dir: %v", err)
	}

	gens, err := List(galeDir, storeRoot)
	if err != nil {
		t.Fatalf("List error: %v", err)
	}

	if len(gens) != 1 {
		t.Fatalf("expected 1 generation, got %d", len(gens))
	}
	if gens[0].Number != 1 {
		t.Errorf("gens[0].Number = %d, want 1", gens[0].Number)
	}
}

func TestDiffShowsAddedAndRemoved(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	createStoreEntry(t, storeRoot, "jq", "1.7.1", []string{"jq"})
	createStoreEntry(t, storeRoot, "fd", "9.0", []string{"fd"})

	// Gen 1: jq only.
	if err := Build(map[string]string{"jq": "1.7.1"}, galeDir, storeRoot); err != nil {
		t.Fatalf("Build gen 1: %v", err)
	}
	// Gen 2: fd only (jq removed).
	if err := Build(map[string]string{"fd": "9.0"}, galeDir, storeRoot); err != nil {
		t.Fatalf("Build gen 2: %v", err)
	}

	d, err := Diff(galeDir, storeRoot, 1, 2)
	if err != nil {
		t.Fatalf("Diff error: %v", err)
	}

	if d.From != 1 || d.To != 2 {
		t.Errorf("Diff From=%d To=%d, want 1→2", d.From, d.To)
	}

	if len(d.Added) != 1 || d.Added[0] != "fd@9.0" {
		t.Errorf("Added = %v, want [fd@9.0]", d.Added)
	}
	if len(d.Removed) != 1 || d.Removed[0] != "jq@1.7.1" {
		t.Errorf("Removed = %v, want [jq@1.7.1]", d.Removed)
	}
}

func TestDiffShowsVersionChanges(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	createStoreEntry(t, storeRoot, "jq", "1.7.1", []string{"jq"})
	createStoreEntry(t, storeRoot, "jq", "1.8.0", []string{"jq"})

	// Gen 1: jq 1.7.1.
	if err := Build(map[string]string{"jq": "1.7.1"}, galeDir, storeRoot); err != nil {
		t.Fatalf("Build gen 1: %v", err)
	}
	// Gen 2: jq 1.8.0 (upgraded).
	if err := Build(map[string]string{"jq": "1.8.0"}, galeDir, storeRoot); err != nil {
		t.Fatalf("Build gen 2: %v", err)
	}

	d, err := Diff(galeDir, storeRoot, 1, 2)
	if err != nil {
		t.Fatalf("Diff error: %v", err)
	}

	// Version change shows as both added and removed.
	if len(d.Added) != 1 || d.Added[0] != "jq@1.8.0" {
		t.Errorf("Added = %v, want [jq@1.8.0]", d.Added)
	}
	if len(d.Removed) != 1 || d.Removed[0] != "jq@1.7.1" {
		t.Errorf("Removed = %v, want [jq@1.7.1]", d.Removed)
	}
}

func TestRollbackSwapsCurrent(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	createStoreEntry(t, storeRoot, "jq", "1.7.1", []string{"jq"})
	createStoreEntry(t, storeRoot, "fd", "9.0", []string{"fd"})

	// Build gen 1 and gen 2.
	if err := Build(map[string]string{"jq": "1.7.1"}, galeDir, storeRoot); err != nil {
		t.Fatalf("Build gen 1: %v", err)
	}
	if err := Build(map[string]string{"jq": "1.7.1", "fd": "9.0"}, galeDir, storeRoot); err != nil {
		t.Fatalf("Build gen 2: %v", err)
	}

	// Current should be 2.
	cur, err := Current(galeDir)
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if cur != 2 {
		t.Fatalf("expected current=2, got %d", cur)
	}

	// Rollback to gen 1.
	if err := Rollback(galeDir, 1); err != nil {
		t.Fatalf("Rollback error: %v", err)
	}

	// Current should now be 1.
	cur, err = Current(galeDir)
	if err != nil {
		t.Fatalf("Current after rollback: %v", err)
	}
	if cur != 1 {
		t.Errorf("expected current=1 after rollback, got %d", cur)
	}

	// Gen 1 symlinks should still work.
	jqLink := filepath.Join(galeDir, "current", "bin", "jq")
	if _, err := os.Stat(jqLink); err != nil {
		t.Errorf("jq symlink should be accessible after rollback: %v", err)
	}
}

func TestRollbackNonexistentGeneration(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	createStoreEntry(t, storeRoot, "jq", "1.7.1", []string{"jq"})

	if err := Build(map[string]string{"jq": "1.7.1"}, galeDir, storeRoot); err != nil {
		t.Fatalf("Build: %v", err)
	}

	err := Rollback(galeDir, 99)
	if err == nil {
		t.Fatal("expected Rollback to non-existent generation to return error")
	}
}
