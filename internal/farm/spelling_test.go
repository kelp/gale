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
	if runtime.GOOS == "linux" {
		soname = "libspell.so.4"
	}
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

// A symlinked lib DIRECTORY is the same problem one level up.
// Populate follows it and farms what it finds, and the farm entry
// still names <store>/lib/<soname>, so the entry breaks when this
// store directory is replaced. Resolving that component walks out of
// the store and disowns it.
func TestFarmPredicateDoesNotFollowASymlinkedLibDir(t *testing.T) {
	root := t.TempDir()
	storeDir := filepath.Join(root, "pkg", "p", "1.0-1")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "elsewhere")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	soname := "libdir.4.dylib"
	if runtime.GOOS == "linux" {
		soname = "libdir.so.4"
	}
	if err := os.WriteFile(
		filepath.Join(outside, soname), []byte("x"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	// <store>/lib -> /outside
	if err := os.Symlink(outside, filepath.Join(storeDir, "lib")); err != nil {
		t.Fatal(err)
	}

	farmDir := filepath.Join(root, "lib")
	if err := Populate(storeDir, farmDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Readlink(filepath.Join(farmDir, soname)); err != nil {
		t.Fatalf("Populate did not farm through the symlinked lib: %v", err)
	}

	entry := filepath.Join(storeDir, "lib", soname)
	if !UnderStoreDir(entry, storeDir) {
		t.Error("an entry named under the store was read as outside it")
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
	if err := Depopulate(storeDir, farmDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(farmDir, soname)); !os.IsNotExist(err) {
		t.Errorf("Depopulate left the link behind: %v", err)
	}
}

// An entry that RESOLVES into a store directory does not belong to
// it. Only the directory the entry is spelled under owns it.
//
// The distinguishing shape: q's lib is a symlink to p's store
// directory, so Populate farms q's entries and spells their targets
// under q, while following them lands in p. A predicate that
// resolved the whole path would hand q's live entries to p, and
// Depopulate(p) would then delete them — the cross-package deletion
// class this file exists to prevent, arriving from the opposite
// direction to the fail-open cases above.
func TestFarmPredicateDoesNotClaimAnEntryThatMerelyResolvesIn(t *testing.T) {
	root := t.TempDir()
	pStore := filepath.Join(root, "pkg", "p", "1.0-1")
	qStore := filepath.Join(root, "pkg", "q", "2.0-1")
	if err := os.MkdirAll(pStore, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(qStore, 0o755); err != nil {
		t.Fatal(err)
	}
	soname := "libown.4.dylib"
	if runtime.GOOS == "linux" {
		soname = "libown.so.4"
	}
	if err := os.WriteFile(
		filepath.Join(pStore, soname), []byte("x"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	// q/lib -> p's store directory.
	if err := os.Symlink(pStore, filepath.Join(qStore, "lib")); err != nil {
		t.Fatal(err)
	}

	farmDir := filepath.Join(root, "lib")
	if err := Populate(qStore, farmDir); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(farmDir, soname)
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("Populate did not farm through q's symlinked lib: %v", err)
	}

	if UnderStoreDir(target, pStore) {
		t.Error("an entry spelled under q was claimed by p because it " +
			"resolves into p")
	}
	if !UnderStoreDir(target, qStore) {
		t.Error("q disowned an entry spelled under its own store dir")
	}
	if err := Depopulate(pStore, farmDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Errorf("Depopulate(p) deleted q's live farm entry: %v", err)
	}
}

// CanonicalDir is the one spelling rule the guard and its claim
// builders share. Both halves of it are load-bearing: an existing
// directory resolves, and an absent one keeps its raw spelling
// rather than becoming an error the caller has to interpret.
func TestCanonicalDir(t *testing.T) {
	raw := t.TempDir()
	resolved, err := filepath.EvalSymlinks(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := CanonicalDir(raw); got != resolved {
		t.Errorf("CanonicalDir(%s) = %s, want %s", raw, got, resolved)
	}

	// A directory that does not exist yet — the pre-commit state of
	// every staged placement's final dir.
	absent := filepath.Join(raw, "pkg", "p", "1.0-1")
	if got := CanonicalDir(absent); got != absent {
		t.Errorf("CanonicalDir(%s) = %s, want the raw spelling",
			absent, got)
	}
}
