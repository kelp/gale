package generation

import (
	"os"
	"path/filepath"
	"testing"
)

// gh#219: bin/ is the only namespace a rebuild arbitrates. man pages
// and root-level files keep first-in-sorted-order-wins, silently. The
// answer is a report, not a refusal — two packages shipping
// man/man1/foo.1 is expected (a library and its CLI, a compat shim),
// and a shadowed man page shows the wrong docs rather than running the
// wrong program.

// writeStoreFile creates one file at a gen-relative path inside a
// package's store dir, making its parents.
func writeStoreFile(t *testing.T, pkgDir, rel string) {
	t.Helper()
	path := filepath.Join(pkgDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// issue219Store lays out two packages that ship the same man page and
// the same root-level file, and returns the store root.
func issue219Store(t *testing.T) string {
	t.Helper()
	storeRoot := t.TempDir()
	for _, name := range []string{"alpha", "beta"} {
		pkgDir := filepath.Join(storeRoot, name, "1.0")
		writeStoreFile(t, pkgDir, "man/man1/foo.1")
		writeStoreFile(t, pkgDir, "VERSION")
		writeStoreFile(t, pkgDir, "bin/"+name)
	}
	return storeRoot
}

// issue219Pkgs is the package map both tests build from.
func issue219Pkgs() map[string]string {
	return map[string]string{"alpha": "1.0", "beta": "1.0"}
}

// TestShadowedFilesReportsManPagesAndRootFiles pins the report. Both
// namespaces are enumerated, the winner is the first package in sorted
// order (the one a rebuild links), and the result is sorted by path so
// it never varies with map iteration.
func TestShadowedFilesReportsManPagesAndRootFiles(t *testing.T) {
	storeRoot := issue219Store(t)

	got := ShadowedFiles(issue219Pkgs(), storeRoot)

	want := []FileCollision{
		{Path: "VERSION", Existing: "alpha", Incoming: "beta"},
		{
			Path:     filepath.Join("man", "man1", "foo.1"),
			Existing: "alpha",
			Incoming: "beta",
		},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d collisions %v, want %d %v",
			len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("collision %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestManPageCollisionStillBuilds is the "we did not hard-fail" pin.
// It passes before this change and must keep passing after it: the
// report must not become a refusal. bin/ is the only namespace whose
// collisions refuse a rebuild; every other merged path keeps
// first-in-sorted-order-wins.
func TestManPageCollisionStillBuilds(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := issue219Store(t)

	if err := Build(issue219Pkgs(), galeDir, storeRoot); err != nil {
		t.Fatalf("Build with a duplicate man page: %v — a shadowed man "+
			"page is reported, never refused", err)
	}

	link := filepath.Join(galeDir, "gen", "1", "man", "man1", "foo.1")
	got, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatalf("resolve %s: %v", link, err)
	}
	want, err := filepath.EvalSymlinks(
		filepath.Join(storeRoot, "alpha", "1.0", "man", "man1", "foo.1"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("man/man1/foo.1 resolves to %q, want %q — the first "+
			"package in sorted order provides it", got, want)
	}
}
