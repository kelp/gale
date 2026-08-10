package farm

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

// gh#199: the farm links versioned sonames but never the
// unversioned aliases a library ships beside them, so a binary the
// linker recorded against the alias breaks the next time its
// dependency takes a revision bump. git-remote-http records
// @rpath/libssl.dylib; the farm held only libssl.3.dylib; gc removed
// the openssl revision the per-dep rpath named, and dyld aborted.

func TestIsUnversionedAliasShape(t *testing.T) {
	cases := []struct {
		goos string
		name string
		want bool
	}{
		// The gh#199 names themselves.
		{"darwin", "libssl.dylib", true},
		{"darwin", "libcrypto.dylib", true},
		{"darwin", "libssl.3.dylib", false},
		// Dotted and punctuated stems, mirroring the versioned
		// regex's gh#165/gh#168 cases.
		{"darwin", "libc++.dylib", true},
		{"darwin", "libMagick++-7.Q16HDRI.dylib", true},
		{"darwin", "libMagick++-7.Q16HDRI.5.dylib", false},
		{"darwin", "libpython3.14.dylib", false},
		// Not shared libraries at all.
		{"darwin", "libfoo.a", false},
		{"darwin", "random.txt", false},
		{"darwin", "foo.dylib", false},
		{"darwin", "libfoo.so", false},
		// Decision: darwin only. On Linux DT_NEEDED records the
		// SONAME, so the unversioned .so is a build-time devel
		// symlink that never appears in a runtime reference.
		{"linux", "libfoo.so", false},
		{"linux", "libssl.dylib", false},
		{"linux", "libfoo.so.3", false},
	}
	for _, c := range cases {
		if got := isUnversionedAlias(c.goos, c.name); got != c.want {
			t.Errorf("isUnversionedAlias(%q, %q) = %v, want %v",
				c.goos, c.name, got, c.want)
		}
	}
}

// TestAliasAndVersionedClassesAreDisjoint: the two predicates carve
// the lib namespace into non-overlapping classes. An overlap would
// let one name be farmed under both rules — claimed as a soname and
// dropped as a contested alias in the same rebuild.
func TestAliasAndVersionedClassesAreDisjoint(t *testing.T) {
	names := []string{
		"libssl.dylib", "libssl.3.dylib", "libc++.dylib",
		"libMagick++-7.Q16HDRI.5.dylib", "libpython3.14.dylib",
		"libpython3.14.so.1.0", "libfoo.so", "libfoo.so.3",
		"libusb-1.0.dylib", "libusb-1.0.so.0",
	}
	for _, goos := range []string{"darwin", "linux"} {
		for _, name := range names {
			if isUnversionedAlias(goos, name) &&
				isVersionedFor(goos, name) {
				t.Errorf("%s/%s classifies as both alias and versioned",
					goos, name)
			}
		}
	}
}

// TestPartitionAliasesDropsAContestedName is the openssl/openssl4
// case. Both recipes configure --libdir=lib and both build shared,
// so both ship libssl.dylib. They are different packages, so the
// versioned conflict rule would hard-fail the install — turning a
// coexistence gale-recipes deliberately designed for into an
// outage. Drop the contested alias instead: nobody gets it, which
// is exactly today's behaviour for that name.
func TestPartitionAliasesDropsAContestedName(t *testing.T) {
	providers := []aliasProvider{
		{storeDir: "/g/pkg/openssl/3.6.1-4", aliases: map[string]string{
			"libssl.dylib":    "/g/pkg/openssl/3.6.1-4/lib/libssl.dylib",
			"libcrypto.dylib": "/g/pkg/openssl/3.6.1-4/lib/libcrypto.dylib",
		}},
		{storeDir: "/g/pkg/openssl4/4.0.0-2", aliases: map[string]string{
			"libssl.dylib": "/g/pkg/openssl4/4.0.0-2/lib/libssl.dylib",
		}},
	}

	entries, conflicts := partitionAliases(providers)

	if len(entries) != 1 || entries[0].Name != "libcrypto.dylib" {
		t.Errorf("entries = %+v, want only libcrypto.dylib", entries)
	}
	if len(conflicts) != 1 || conflicts[0].Name != "libssl.dylib" {
		t.Fatalf("conflicts = %+v, want libssl.dylib", conflicts)
	}
	want := []string{"openssl4@4.0.0-2", "openssl@3.6.1-4"}
	if !slices.Equal(conflicts[0].Owners, want) {
		t.Errorf("owners = %v, want %v (sorted)",
			conflicts[0].Owners, want)
	}
}

// TestPartitionAliasesIsOrderIndependent: the partition is a pure
// function of the whole set, so install order and map order cannot
// decide which package wins a farm entry (940a67a).
func TestPartitionAliasesIsOrderIndependent(t *testing.T) {
	a := aliasProvider{storeDir: "/g/pkg/openssl/3.6.1-4",
		aliases: map[string]string{
			"libssl.dylib": "/g/pkg/openssl/3.6.1-4/lib/libssl.dylib",
			"libz.dylib":   "/g/pkg/openssl/3.6.1-4/lib/libz.dylib",
		}}
	b := aliasProvider{storeDir: "/g/pkg/openssl4/4.0.0-2",
		aliases: map[string]string{
			"libssl.dylib": "/g/pkg/openssl4/4.0.0-2/lib/libssl.dylib",
		}}

	fwdEntries, fwdConflicts := partitionAliases([]aliasProvider{a, b})
	revEntries, revConflicts := partitionAliases([]aliasProvider{b, a})

	if !slices.Equal(fwdEntries, revEntries) {
		t.Errorf("entries differ by input order:\n %+v\n %+v",
			fwdEntries, revEntries)
	}
	if len(fwdConflicts) != len(revConflicts) {
		t.Fatalf("conflict count differs by order: %d vs %d",
			len(fwdConflicts), len(revConflicts))
	}
	for i := range fwdConflicts {
		if fwdConflicts[i].Name != revConflicts[i].Name ||
			!slices.Equal(fwdConflicts[i].Owners, revConflicts[i].Owners) {
			t.Errorf("conflict %d differs by order: %+v vs %+v",
				i, fwdConflicts[i], revConflicts[i])
		}
	}
}

// TestPartitionAliasesKeysOnStoreDirNotPackageName: two revisions of
// ONE package can both reach the rebuild union when they provide
// different sonames (libssl.3 vs libssl.1.1). No versioned conflict
// fires to catch that, and both resolve libssl.dylib to
// incompatible targets — so the collision key is the store dir.
func TestPartitionAliasesKeysOnStoreDirNotPackageName(t *testing.T) {
	providers := []aliasProvider{
		{storeDir: "/g/pkg/openssl/3.6.1-4", aliases: map[string]string{
			"libssl.dylib": "/g/pkg/openssl/3.6.1-4/lib/libssl.dylib",
		}},
		{storeDir: "/g/pkg/openssl/1.1.1w-1", aliases: map[string]string{
			"libssl.dylib": "/g/pkg/openssl/1.1.1w-1/lib/libssl.dylib",
		}},
	}

	entries, conflicts := partitionAliases(providers)

	if len(entries) != 0 {
		t.Errorf("entries = %+v, want none: two store dirs claim it",
			entries)
	}
	if len(conflicts) != 1 {
		t.Fatalf("conflicts = %+v, want libssl.dylib contested",
			conflicts)
	}
}

// TestPartitionAliasesFarmsASoleProvider is the other half of the
// drop rule, and the thing that makes removal self-healing: once the
// second claimant is gone from the closure the alias is farmable
// again, with no special-case un-drop path.
func TestPartitionAliasesFarmsASoleProvider(t *testing.T) {
	sole := []aliasProvider{
		{storeDir: "/g/pkg/openssl/3.6.1-4", aliases: map[string]string{
			"libssl.dylib": "/g/pkg/openssl/3.6.1-4/lib/libssl.dylib",
		}},
	}

	entries, conflicts := partitionAliases(sole)

	if len(conflicts) != 0 {
		t.Errorf("conflicts = %+v, want none", conflicts)
	}
	if len(entries) != 1 ||
		entries[0].StoreDir != "/g/pkg/openssl/3.6.1-4" ||
		entries[0].Target != "/g/pkg/openssl/3.6.1-4/lib/libssl.dylib" {
		t.Errorf("entries = %+v, want libssl.dylib from openssl", entries)
	}
}

// TestAliasTargetAcceptsAnInheritedPromise: an unversioned name
// carries no ABI promise of its own, so it is farmable exactly when
// it points at a versioned soname that has one.
func TestAliasTargetAcceptsAnInheritedPromise(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("alias farming is darwin-only")
	}
	root := t.TempDir()

	t.Run("symlink to a versioned sibling", func(t *testing.T) {
		storeDir := storeLayout(t, root, "openssl", "3.6.1-4",
			[]string{"libssl.3.dylib"})
		libDir := filepath.Join(storeDir, "lib")
		mustSymlink(t, "libssl.3.dylib",
			filepath.Join(libDir, "libssl.dylib"))

		got, ok := aliasTarget(libDir, "libssl.dylib")
		if !ok {
			t.Fatal("libssl.dylib -> libssl.3.dylib should be farmable")
		}
		// The farm entry targets the ALIAS inside the package, not
		// what it resolves to — the rule Populate already applies
		// to versioned aliases and the one UnderStoreDir rests on.
		if want := filepath.Join(libDir, "libssl.dylib"); got != want {
			t.Errorf("target = %q, want %q", got, want)
		}
	})

	// The layout spelling_test.go pins: the versioned soname inside
	// the store is itself a symlink to a real file OUTSIDE the
	// store, and is still farmed. Resolving the whole chain and
	// demanding the real file sit in this lib dir would make
	// openssl's alias unfarmable in exactly that layout — which is
	// why the predicate is one hop, then Stat.
	t.Run("versioned hop may itself leave the store", func(t *testing.T) {
		storeDir := storeLayout(t, root, "indirect", "1.0-1", nil)
		libDir := filepath.Join(storeDir, "lib")
		realFile := filepath.Join(root, "libindirect.real.dylib")
		if err := os.WriteFile(realFile, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		mustSymlink(t, realFile,
			filepath.Join(libDir, "libindirect.3.dylib"))
		mustSymlink(t, "libindirect.3.dylib",
			filepath.Join(libDir, "libindirect.dylib"))

		if _, ok := aliasTarget(libDir, "libindirect.dylib"); !ok {
			t.Error("one hop inside the lib dir is the rule; the " +
				"versioned soname may resolve anywhere")
		}
	})
}

// TestAliasTargetRejectsWhatInheritsNothing: every way an
// unversioned name can fail to inherit a promise. Farming one of
// these would give dyld a stable path to something with no
// compatibility guarantee behind it — worse than today's clean
// failure to resolve.
func TestAliasTargetRejectsWhatInheritsNothing(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("alias farming is darwin-only")
	}
	root := t.TempDir()

	t.Run("regular file", func(t *testing.T) {
		storeDir := storeLayout(t, root, "solo", "1.0-1",
			[]string{"libsolo.dylib"})
		if _, ok := aliasTarget(filepath.Join(storeDir, "lib"),
			"libsolo.dylib"); ok {
			t.Error("a real unversioned file inherits no soname's " +
				"promise and must not be farmed")
		}
	})

	t.Run("symlink to an unversioned name", func(t *testing.T) {
		storeDir := storeLayout(t, root, "chain", "1.0-1",
			[]string{"libother.dylib"})
		libDir := filepath.Join(storeDir, "lib")
		mustSymlink(t, "libother.dylib",
			filepath.Join(libDir, "libchain.dylib"))

		if _, ok := aliasTarget(libDir, "libchain.dylib"); ok {
			t.Error("hop must land on a versioned soname")
		}
	})

	t.Run("symlink escaping the lib dir", func(t *testing.T) {
		storeDir := storeLayout(t, root, "escape", "1.0-1", nil)
		libDir := filepath.Join(storeDir, "lib")
		outside := filepath.Join(root, "libescape.3.dylib")
		if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		mustSymlink(t, outside, filepath.Join(libDir, "libescape.dylib"))

		if _, ok := aliasTarget(libDir, "libescape.dylib"); ok {
			t.Error("first hop must stay inside this package's lib dir")
		}
	})

	t.Run("dangling symlink", func(t *testing.T) {
		storeDir := storeLayout(t, root, "dangle", "1.0-1", nil)
		libDir := filepath.Join(storeDir, "lib")
		mustSymlink(t, "libdangle.3.dylib",
			filepath.Join(libDir, "libdangle.dylib"))

		if _, ok := aliasTarget(libDir, "libdangle.dylib"); ok {
			t.Error("a farm entry chain ending nowhere is a broken lib")
		}
	})
}

// TestRebuildFarmsUnversionedAlias is the gh#199 repro at the layer
// that fixes it: the closure, not the package. Populate sees one
// store dir and cannot know whether a second provider exists, so
// the alias decision belongs to Rebuild.
func TestRebuildFarmsUnversionedAlias(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("alias farming is darwin-only")
	}
	root := t.TempDir()
	farmDir := filepath.Join(root, "lib")
	storeDir := storeLayout(t, root, "openssl", "3.6.1-4",
		[]string{"libssl.3.dylib"})
	mustSymlink(t, "libssl.3.dylib",
		filepath.Join(storeDir, "lib", "libssl.dylib"))

	if err := Rebuild([]string{storeDir}, farmDir); err != nil {
		t.Fatal(err)
	}

	target, err := os.Readlink(filepath.Join(farmDir, "libssl.dylib"))
	if err != nil {
		t.Fatalf("alias not farmed: %v", err)
	}
	if want := filepath.Join(storeDir, "lib", "libssl.dylib"); target != want {
		t.Errorf("target = %q, want %q", target, want)
	}
	if _, err := os.Readlink(
		filepath.Join(farmDir, "libssl.3.dylib"),
	); err != nil {
		t.Errorf("versioned soname must still be farmed: %v", err)
	}
}

// TestRebuildDropsAnAliasTwoPackagesProvide: the drop must not be an
// error. gh#42 made a versioned collision fail the install on every
// path; routing aliases through that rule would make openssl4
// uninstallable alongside openssl.
func TestRebuildDropsAnAliasTwoPackagesProvide(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("alias farming is darwin-only")
	}
	root := t.TempDir()
	farmDir := filepath.Join(root, "lib")
	ssl3 := storeLayout(t, root, "openssl", "3.6.1-4",
		[]string{"libssl.3.dylib"})
	mustSymlink(t, "libssl.3.dylib",
		filepath.Join(ssl3, "lib", "libssl.dylib"))
	ssl4 := storeLayout(t, root, "openssl4", "4.0.0-2",
		[]string{"libssl.4.dylib"})
	mustSymlink(t, "libssl.4.dylib",
		filepath.Join(ssl4, "lib", "libssl.dylib"))

	if err := Rebuild([]string{ssl3, ssl4}, farmDir); err != nil {
		t.Fatalf("an alias collision must not fail the rebuild: %v", err)
	}

	if _, err := os.Lstat(filepath.Join(farmDir, "libssl.dylib")); err == nil {
		t.Error("contested alias must be farmed for neither package")
	}
	for _, soname := range []string{"libssl.3.dylib", "libssl.4.dylib"} {
		if _, err := os.Readlink(filepath.Join(farmDir, soname)); err != nil {
			t.Errorf("versioned %s must still be farmed: %v", soname, err)
		}
	}
}

// TestRebuildRestoresTheAliasWhenTheOtherClaimantLeaves is the
// remove path. Nothing un-drops an alias explicitly — the rebuild
// recomputes the partition from the new closure, which is the only
// reason removal heals. Pinned so a future "skip the rebuild when
// nothing changed" optimisation cannot silently break it.
func TestRebuildRestoresTheAliasWhenTheOtherClaimantLeaves(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("alias farming is darwin-only")
	}
	root := t.TempDir()
	farmDir := filepath.Join(root, "lib")
	ssl3 := storeLayout(t, root, "openssl", "3.6.1-4",
		[]string{"libssl.3.dylib"})
	mustSymlink(t, "libssl.3.dylib",
		filepath.Join(ssl3, "lib", "libssl.dylib"))
	ssl4 := storeLayout(t, root, "openssl4", "4.0.0-2",
		[]string{"libssl.4.dylib"})
	mustSymlink(t, "libssl.4.dylib",
		filepath.Join(ssl4, "lib", "libssl.dylib"))

	if err := Rebuild([]string{ssl3, ssl4}, farmDir); err != nil {
		t.Fatal(err)
	}
	if err := Rebuild([]string{ssl3}, farmDir); err != nil {
		t.Fatal(err)
	}

	target, err := os.Readlink(filepath.Join(farmDir, "libssl.dylib"))
	if err != nil {
		t.Fatalf("alias should be farmable once uncontested: %v", err)
	}
	if want := filepath.Join(ssl3, "lib", "libssl.dylib"); target != want {
		t.Errorf("target = %q, want %q", target, want)
	}
}

// TestPopulateDoesNotFarmAliases: the alias decision needs the whole
// closure, and Populate is handed one store dir. Keeping it out of
// Populate is what makes the drop stable rather than provisional.
func TestPopulateDoesNotFarmAliases(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("alias farming is darwin-only")
	}
	root := t.TempDir()
	farmDir := filepath.Join(root, "lib")
	storeDir := storeLayout(t, root, "openssl", "3.6.1-4",
		[]string{"libssl.3.dylib"})
	mustSymlink(t, "libssl.3.dylib",
		filepath.Join(storeDir, "lib", "libssl.dylib"))

	if err := Populate(storeDir, farmDir); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Lstat(filepath.Join(farmDir, "libssl.dylib")); err == nil {
		t.Error("Populate must leave aliases to the closure-level pass")
	}
	if _, err := os.Readlink(
		filepath.Join(farmDir, "libssl.3.dylib"),
	); err != nil {
		t.Errorf("versioned soname must still be farmed: %v", err)
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}
