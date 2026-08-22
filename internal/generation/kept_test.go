package generation

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/store"
)

func TestKeptNumbersCurrentAndPreviousOnly(t *testing.T) {
	galeDir := t.TempDir()
	stageGenNumbers(t, galeDir, []int{1, 4, 5, 9, 10}, 5)

	got, err := KeptNumbers(galeDir)
	if err != nil {
		t.Fatalf("KeptNumbers: %v", err)
	}
	if want := []int{4, 5}; !slices.Equal(got, want) {
		t.Errorf("KeptNumbers(cur=5) = %v, want %v — current and "+
			"one previous; abandoned gens above current are not kept",
			got, want)
	}
}

func TestKeptNumbersCountsPreviousPositionally(t *testing.T) {
	galeDir := t.TempDir()
	stageGenNumbers(t, galeDir, []int{1, 5, 9}, 5)

	got, err := KeptNumbers(galeDir)
	if err != nil {
		t.Fatalf("KeptNumbers: %v", err)
	}
	if want := []int{1, 5}; !slices.Equal(got, want) {
		t.Errorf("KeptNumbers(cur=5, gens 1/5/9) = %v, want %v — "+
			"keep-2 is the previous EXISTING generation, not "+
			"cur-1 (gen/4 is absent; gen/1 is the previous)",
			got, want)
	}
}

func TestKeptNumbersDoesNotInventAbsentPrevious(t *testing.T) {
	galeDir := t.TempDir()
	stageGenNumbers(t, galeDir, []int{1, 5, 9}, 5)

	got, err := KeptNumbers(galeDir)
	if err != nil {
		t.Fatalf("KeptNumbers: %v", err)
	}
	if slices.Contains(got, 4) {
		t.Errorf("KeptNumbers = %v, must not invent gen/4", got)
	}
	if want := []int{1, 5}; !slices.Equal(got, want) {
		t.Errorf("KeptNumbers(cur=5, no gen/4) = %v, want %v", got, want)
	}
}

func TestKeptNumbersNoCurrentIsEmpty(t *testing.T) {
	galeDir := t.TempDir()
	stageGenNumbers(t, galeDir, []int{1, 2, 3}, 0)

	got, err := KeptNumbers(galeDir)
	if err != nil {
		t.Fatalf("KeptNumbers: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("KeptNumbers(no current) = %v, want empty", got)
	}
}

func TestKeptNumbersIncludesAbsentCurrent(t *testing.T) {
	galeDir := t.TempDir()
	stageGenNumbers(t, galeDir, []int{1, 2}, 2)
	if err := os.RemoveAll(filepath.Join(galeDir, "gen", "2")); err != nil {
		t.Fatal(err)
	}

	got, err := KeptNumbers(galeDir)
	if err != nil {
		t.Fatalf("KeptNumbers: %v", err)
	}
	if !slices.Contains(got, 2) {
		t.Errorf("KeptNumbers = %v, must include absent current 2", got)
	}
}

func TestKeptStoreDirsReturnsFetchSHA12(t *testing.T) {
	storeRoot := t.TempDir()
	galeDir := filepath.Join(t.TempDir(), ".gale")
	sha := strings.Repeat("ab", 32)
	st := store.NewStore(storeRoot)
	fetchDir, err := st.FetchPath("jq", "1.8.1", sha)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(fetchDir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fetchDir, "bin", "jq"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := BuildWithOptions(
		map[string]string{"jq": "1.8.1"},
		galeDir, storeRoot,
		Options{Fetch: map[string]string{"jq": sha}},
	); err != nil {
		t.Fatalf("Build: %v", err)
	}

	got, err := KeptStoreDirs(galeDir, storeRoot)
	if err != nil {
		t.Fatalf("KeptStoreDirs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("KeptStoreDirs = %v, want one fetch path", got)
	}
	wantRel := filepath.Join(store.FetchNamespace, "jq", "1.8.1-"+sha[:12])
	if !strings.HasSuffix(got[0], wantRel) {
		t.Errorf("KeptStoreDirs[0] = %q, want suffix %q", got[0], wantRel)
	}
}

func TestKeptStoreDirsUnionsTwoKeptGens(t *testing.T) {
	storeRoot := t.TempDir()
	galeDir := filepath.Join(t.TempDir(), ".gale")
	createStoreEntry(t, storeRoot, "jq", "1.6", []string{"jq"})
	createStoreEntry(t, storeRoot, "jq", "1.7", []string{"jq"})
	createStoreEntry(t, storeRoot, "jq", "1.8", []string{"jq"})
	if err := Build(map[string]string{"jq": "1.6"}, galeDir, storeRoot); err != nil {
		t.Fatalf("Build gen 1: %v", err)
	}
	if err := Build(map[string]string{"jq": "1.7"}, galeDir, storeRoot); err != nil {
		t.Fatalf("Build gen 2: %v", err)
	}
	if err := Build(map[string]string{"jq": "1.8"}, galeDir, storeRoot); err != nil {
		t.Fatalf("Build gen 3: %v", err)
	}

	got, err := KeptStoreDirs(galeDir, storeRoot)
	if err != nil {
		t.Fatalf("KeptStoreDirs: %v", err)
	}
	rels := make([]string, 0, len(got))
	for _, dir := range got {
		rel, err := filepath.Rel(storeRoot, dir)
		if err != nil {
			t.Fatal(err)
		}
		rels = append(rels, rel)
	}
	slices.Sort(rels)
	if want := []string{"jq/1.7", "jq/1.8"}; !slices.Equal(rels, want) {
		t.Errorf("KeptStoreDirs rels = %v, want %v — gen/1 is not kept",
			rels, want)
	}
}

func TestKeptStoreDirsRefusesUnreadableKeptGeneration(t *testing.T) {
	storeRoot := t.TempDir()
	galeDir := filepath.Join(t.TempDir(), ".gale")
	createStoreEntry(t, storeRoot, "jq", "1.7", []string{"jq"})
	if err := Build(map[string]string{"jq": "1.7"}, galeDir, storeRoot); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(galeDir, "gen", "1")); err != nil {
		t.Fatal(err)
	}

	got, err := KeptStoreDirs(galeDir, storeRoot)
	if err == nil {
		t.Fatalf("KeptStoreDirs must refuse an unreadable kept generation, got %v", got)
	}
	if !strings.Contains(err.Error(), "generation 1") {
		t.Errorf("error must name generation 1, got: %v", err)
	}
	if strings.Contains(err.Error(), "--force") {
		t.Errorf("error must not advertise --force, got: %v", err)
	}
}
