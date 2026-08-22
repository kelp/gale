package generation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/store"
)

func farmLibDir(storeRoot string) string {
	return filepath.Join(filepath.Dir(storeRoot), "lib")
}

func TestBuildFetchDoesNotCreateFarmLib(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := filepath.Join(galeDir, "pkg")
	if err := os.MkdirAll(storeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("ab", 32)
	st := store.NewStore(storeRoot)
	fetchDir, err := st.FetchPath("jq", "1.8.1", sha)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(fetchDir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fetchDir, "bin", "jq"), []byte("fetch"), 0o755); err != nil {
		t.Fatal(err)
	}

	err = BuildWithOptions(
		map[string]string{"jq": "1.8.1"},
		galeDir, storeRoot,
		Options{Fetch: map[string]string{"jq": sha}},
	)
	if err != nil {
		t.Fatalf("BuildWithOptions: %v", err)
	}

	current := filepath.Join(galeDir, "current")
	info, err := os.Lstat(current)
	if err != nil {
		t.Fatalf("current missing: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("current must be a symlink")
	}
	link, err := os.Readlink(filepath.Join(current, "bin", "jq"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(link, filepath.Join("fetch", "jq")) {
		t.Errorf("link %q does not point at fetch tree", link)
	}
	if _, err := os.Stat(farmLibDir(storeRoot)); !os.IsNotExist(err) {
		t.Errorf("Build must not create %s, err=%v", farmLibDir(storeRoot), err)
	}
}

func TestRollbackDoesNotCreateFarmLib(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := filepath.Join(galeDir, "pkg")
	if err := os.MkdirAll(storeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	createStoreEntry(t, storeRoot, "jq", "1.1-1", []string{"jq"})
	createStoreEntry(t, storeRoot, "jq", "1.2-1", []string{"jq"})
	if err := Build(map[string]string{"jq": "1.1-1"}, galeDir, storeRoot); err != nil {
		t.Fatalf("Build 1: %v", err)
	}
	if err := Build(map[string]string{"jq": "1.2-1"}, galeDir, storeRoot); err != nil {
		t.Fatalf("Build 2: %v", err)
	}
	lib := farmLibDir(storeRoot)
	_ = os.RemoveAll(lib)

	if err := Rollback(galeDir, storeRoot, 1); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if _, err := os.Stat(lib); !os.IsNotExist(err) {
		t.Errorf("Rollback must not create %s, err=%v", lib, err)
	}
	got, err := os.Readlink(filepath.Join(galeDir, "current"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "1") && !strings.Contains(got, string(filepath.Separator)+"1") {
		t.Errorf("current = %q, want generation 1", got)
	}
}
