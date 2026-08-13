package farm

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// gh#184: a rebuild used to wipe the farm and repopulate it in
// place. Two properties fell out of that, and both are user-visible.
//
// A rebuild that cannot build the whole image left the farm
// DESTROYED rather than unchanged, so a failure was strictly worse
// than doing nothing: every binary on the machine lost the entries
// the rebuild never got to recreate.
//
// And even a fully successful rebuild made every entry absent for
// the duration, so a dlopen racing the rebuild could miss a library
// nothing was changing.
//
// The image is therefore staged beside the farm and published entry
// by entry, which is what these two tests pin.

// unreadableLibStoreDir lays out a store dir whose lib is a FILE.
// Populate cannot read it, so it stands in for any per-package
// population failure. A file rather than an unreadable directory
// because CI and the agent container run as root, where mode bits
// are bypassed and a chmod fixture passes for the wrong reason.
func unreadableLibStoreDir(t *testing.T, root string) string {
	t.Helper()
	dir := filepath.Join(root, "pkg", "bad", "2.0-1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(dir, "lib"), []byte("not a dir"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	return dir
}

// backdateLink stamps a farm entry's own mtime (Lutimes, so the
// symlink is stamped rather than its target) far enough in the past
// that recreating it is unmistakable, and returns the stamp.
//
// The mark is the mtime and not the inode. Inode identity is the
// obvious proxy for "this link was never removed", and it is a false
// one: both Linux and macOS hand a freed inode number straight back
// to the next allocation, so a wipe-and-recreate of the same two
// links reproduces the same two inode numbers and the check passes
// while the property is broken.
func backdateLink(t *testing.T, path string) time.Time {
	t.Helper()
	stamp := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	tv := unix.NsecToTimeval(stamp.UnixNano())
	if err := unix.Lutimes(path, []unix.Timeval{tv, tv}); err != nil {
		t.Fatalf("stamping %s: %v", path, err)
	}
	return stamp
}

// linkMtime reads a farm entry's own mtime.
func linkMtime(t *testing.T, path string) time.Time {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("farm entry missing: %v", err)
	}
	return info.ModTime()
}

// A rebuild that cannot build the whole image publishes none of it.
//
// The farm keeps describing the state the rebuild failed to replace,
// which is a state whose bytes are still on disk. Publishing the
// half that succeeded would delete entries no one asked to change,
// and the caller's error would then be describing a farm it had
// already broken.
func TestRebuildPublishesNothingOnAPopulationFailure(t *testing.T) {
	root := t.TempDir()
	farmDir := filepath.Join(root, "lib")

	// An entry the failing rebuild does not even mention, standing in
	// for every other scope's claim living in the shared farm.
	seeded := versionedName("libseed", "1")
	seed := storeLayout(t, root, "seed", "1.0-1", []string{seeded})
	if err := Populate(seed, farmDir); err != nil {
		t.Fatal(err)
	}
	before, err := os.Readlink(filepath.Join(farmDir, seeded))
	if err != nil {
		t.Fatal(err)
	}

	soname := versionedName("libgood", "1")
	good := storeLayout(t, root, "good", "1.0-1", []string{soname})
	bad := unreadableLibStoreDir(t, root)

	if err := Rebuild([]string{good, bad}, farmDir); err == nil {
		t.Fatal("Rebuild swallowed a population failure")
	}

	after, err := os.Readlink(filepath.Join(farmDir, seeded))
	if err != nil {
		t.Fatalf("a failed rebuild destroyed an entry it never "+
			"claimed to change: %v", err)
	}
	if after != before {
		t.Errorf("farm entry moved to %s, want the pre-rebuild %s",
			after, before)
	}
	if _, err := os.Lstat(filepath.Join(farmDir, soname)); err == nil {
		t.Errorf("a failed rebuild published %s from a partial image",
			soname)
	}
}

// A successful rebuild leaves an unchanged entry alone.
//
// This is the machine-checkable proxy for "never absent". A rebuild
// that recreates every entry has, for some interval, no entry at
// all, and a dlopen landing in that interval fails over a library
// the rebuild was not changing. Nothing in a Go test can observe
// that interval; that the link object survived the rebuild is the
// property that rules it out.
func TestRebuildLeavesAnUnchangedEntryUntouched(t *testing.T) {
	root := t.TempDir()
	farmDir := filepath.Join(root, "lib")

	soname := versionedName("libsteady", "1")
	steady := storeLayout(t, root, "steady", "1.0-1", []string{soname})
	if err := Rebuild([]string{steady}, farmDir); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(farmDir, soname)
	before := backdateLink(t, entry)

	// A second rebuild that adds a package and changes nothing about
	// the first one.
	other := storeLayout(t, root, "other", "2.0-1",
		[]string{versionedName("libother", "2")})
	if err := Rebuild([]string{steady, other}, farmDir); err != nil {
		t.Fatal(err)
	}

	if after := linkMtime(t, entry); !after.Equal(before) {
		t.Errorf("unchanged entry %s was recreated (mtime %s -> %s); "+
			"it was absent for the duration of the rebuild",
			soname, before, after)
	}
}
