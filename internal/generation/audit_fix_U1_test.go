package generation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildSkipsEmptyInFlightRevisionDir is the generation-level
// repro for gh#76: the store holds a populated jq/1.8.1-2 and an
// empty jq/1.8.1-3 (pre-created by a concurrent install, or left
// behind by a killed one). A generation rebuild that resolves the
// bare config pin "1.8.1" must link the populated revision —
// before the fix it picked the empty dir, emitted zero symlinks,
// and silently dropped jq from PATH.
func TestBuildSkipsEmptyInFlightRevisionDir(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()

	createStoreEntry(t, storeRoot, "jq", "1.8.1-2", []string{"jq"})
	if err := os.MkdirAll(
		filepath.Join(storeRoot, "jq", "1.8.1-3"), 0o755,
	); err != nil {
		t.Fatal(err)
	}

	if err := Build(
		map[string]string{"jq": "1.8.1"}, galeDir, storeRoot,
	); err != nil {
		t.Fatalf("Build: %v", err)
	}

	target, err := os.Readlink(
		filepath.Join(galeDir, "gen", "1", "bin", "jq"),
	)
	if err != nil {
		t.Fatalf("jq dropped from generation (gh#76): %v", err)
	}
	wantFragment := filepath.Join("jq", "1.8.1-2", "bin", "jq")
	if !strings.Contains(target, wantFragment) {
		t.Errorf("jq symlink target = %q, want fragment %q",
			target, wantFragment)
	}
}
