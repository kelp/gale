package farm

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A store directory reached through a different but equivalent
// spelling must produce the same answer.
//
// The comparison is fail-open when it is spelling-sensitive: a
// caller holding a canonicalized store dir gets an empty result and
// concludes there is nothing to guard and nothing to remove, so the
// dangling links this machinery exists to remove survive silently.
// On macOS /var and /private/var are the same directory, and every
// temp dir in this suite sits under one.
func TestFarmPredicateIgnoresPathSpelling(t *testing.T) {
	raw := t.TempDir()
	resolved, err := filepath.EvalSymlinks(raw)
	if err != nil {
		t.Fatal(err)
	}
	if resolved == raw {
		t.Skip("no symlinked temp prefix on this machine")
	}

	storeDir := filepath.Join(raw, "pkg", "p", "1.0-1")
	lib := filepath.Join(storeDir, "lib")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	soname := "libspell.4.dylib"
	if err := os.WriteFile(
		filepath.Join(lib, soname), []byte("x"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	farmDir := filepath.Join(raw, "lib")
	if err := Populate(storeDir, farmDir); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(resolved, "pkg", "p", "1.0-1")

	staging := filepath.Join(t.TempDir(), "staged")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, dir string }{
		{"as written", storeDir},
		{"resolved", other},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stale, err := StaleLinks(tc.dir, staging, farmDir)
			if err != nil {
				t.Fatal(err)
			}
			if len(stale) != 1 || stale[0] != soname {
				t.Errorf("StaleLinks = %v, want [%s]", stale, soname)
			}
		})
	}

	// Depopulate decides with the same predicate, so it must agree.
	if err := Depopulate(other, farmDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(farmDir, soname)); !os.IsNotExist(err) {
		t.Errorf("Depopulate left the link behind: %v", err)
	}
}

// A farmed entry whose target is a versioned symlink ALIAS belongs
// to the store directory the target names, whatever the alias
// resolves to.
//
// Populate farms aliases on purpose, because that is how libtool
// ships a soname and what DT_NEEDED and Mach-O install names
// reference. A predicate that followed the alias could be led out of
// the store by a link that plainly lives inside it, report the entry
// as somebody else's, and leave it dangling after the directory is
// replaced.
func TestFarmPredicateDoesNotFollowTheFarmedAlias(t *testing.T) {
	root := t.TempDir()
	storeDir := filepath.Join(root, "pkg", "p", "1.0-1")
	lib := filepath.Join(storeDir, "lib")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	// The real file lives outside the store entirely.
	outside := filepath.Join(root, "elsewhere")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "libalias.real")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	soname := "libalias.4.dylib"
	if runtime.GOOS == "linux" {
		soname = "libalias.so.4"
	}
	alias := filepath.Join(lib, soname)
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}

	farmDir := filepath.Join(root, "lib")
	if err := Populate(storeDir, farmDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Readlink(filepath.Join(farmDir, soname)); err != nil {
		t.Fatalf("Populate did not farm the alias: %v", err)
	}

	if !UnderStoreDir(alias, storeDir) {
		t.Error("a farmed alias inside the store was read as outside it")
	}
	staging := filepath.Join(t.TempDir(), "staged")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	stale, err := StaleLinks(storeDir, staging, farmDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 1 || stale[0] != soname {
		t.Errorf("StaleLinks = %v, want [%s]", stale, soname)
	}
}
