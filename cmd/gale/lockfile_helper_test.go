package main

// Tests for updateLockfile, the helper that runSyncOne calls to
// persist SHA256 hashes after a successful install, and for
// WriteConfigAndLock's manifest-digest handling.
//
// The write-failure tests exercise existing production code and
// pass. The manifest-digest tests are RED until updateLockfile
// persists its manifestDigest parameter and WriteConfigAndLock
// threads digests through the relock path.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kelp/gale/internal/lockfile"
)

// TestUpdateLockfileSurfacesWriteFailure pins the contract that
// updateLockfile returns a non-nil error when the lockfile cannot
// be written. runSyncOne stores that error in outcome.lockfileErr
// rather than returning it (non-fatal), so the two tests together
// ground the behaviour:
//
//  1. updateLockfile surfaces write errors (this test).
//  2. runSyncOne captures the error in lockfileErr without
//     aborting the overall outcome (asserted indirectly by the
//     install-failure path in sync_one_test.go).
//
// Strategy: make the lockfile path itself a directory, so the write
// fails with EISDIR even though the parent exists. The filelock
// implementation creates parent dirs via MkdirAll, so a missing
// parent is not a reliable failure path.
func TestUpdateLockfileSurfacesWriteFailure(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: EISDIR check may not apply")
	}

	tmp := t.TempDir()

	// Create a directory at the lockfile path so Write fails
	// with EISDIR — the parent exists (satisfying filelock.With's
	// MkdirAll), but the path itself is not a regular file.
	lockPath := filepath.Join(tmp, "gale.lock")
	if err := os.MkdirAll(lockPath, 0o755); err != nil {
		t.Fatal(err)
	}
	// filelock also needs to open lockPath+".lock" — that will
	// succeed as a file alongside our dir. The read of lockPath
	// will fail or return empty; the write of lockPath will fail
	// because it is a directory.
	err := updateLockfile(lockPath, "pkg", "1.0.0", "deadbeef", "")
	if err == nil {
		t.Error("updateLockfile returned nil, want non-nil error " +
			"when the lockfile path is a directory")
	}
}

// TestUpdateLockfileSurfacesWriteFailureUnwritableDir verifies that
// updateLockfile also surfaces errors when the parent directory
// exists but is not writable (mode 0o500).
func TestUpdateLockfileSurfacesWriteFailureUnwritableDir(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: permission checks do not apply")
	}

	tmp := t.TempDir()
	roDir := filepath.Join(tmp, "readonly")
	if err := os.MkdirAll(roDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Make the directory read-only so writes inside it fail.
	if err := os.Chmod(roDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(roDir, 0o755) })

	lockPath := filepath.Join(roDir, "gale.lock")

	err := updateLockfile(lockPath, "pkg", "1.0.0", "deadbeef", "")
	if err == nil {
		t.Error("updateLockfile returned nil, want non-nil error " +
			"when the parent directory is not writable")
	}
}

// testManifestDigest is a well-formed OCI manifest digest for
// round-trip assertions.
const testManifestDigest = "sha256:" +
	"9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

// TestUpdateLockfilePersistsManifestDigest pins that the manifest
// digest handed to updateLockfile lands on the LockedPackage entry
// and survives a read-back, so `gale verify` can pin by digest.
func TestUpdateLockfilePersistsManifestDigest(t *testing.T) {
	tmp := t.TempDir()
	lockPath := filepath.Join(tmp, "gale.lock")

	if err := updateLockfile(
		lockPath, "pkg", "1.0.0-2", "deadbeef", testManifestDigest,
	); err != nil {
		t.Fatalf("updateLockfile: %v", err)
	}

	got, err := lockfile.Read(lockPath)
	if err != nil {
		t.Fatalf("lockfile.Read: %v", err)
	}
	entry, ok := got.Packages["pkg"]
	if !ok {
		t.Fatal("pkg entry missing after update")
	}
	if entry.Version != "1.0.0-2" {
		t.Errorf("Version = %q, want %q", entry.Version, "1.0.0-2")
	}
	if entry.SHA256 != "deadbeef" {
		t.Errorf("SHA256 = %q, want %q", entry.SHA256, "deadbeef")
	}
	if entry.ManifestDigest != testManifestDigest {
		t.Errorf("ManifestDigest = %q, want %q",
			entry.ManifestDigest, testManifestDigest)
	}
}

// TestWriteConfigAndLockRelockPreservesManifestDigest pins the
// cached-install rewrite path (sha256 == ""): when the existing
// lock entry has a bare version ("2.53.0") and the resolver hands
// us the canonical "2.53.0-2", WriteConfigAndLock rewrites the
// entry to the canonical pin while carrying the old hash forward.
// The manifest digest must survive that rewrite too — dropping it
// would silently downgrade a digest-pinned package to tag-based
// verification on the next cached sync.
func TestWriteConfigAndLockRelockPreservesManifestDigest(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "gale.toml")
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n  git = \"2.53.0\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	// Input lock: bare version, hash, and a manifest digest.
	lockPath := filepath.Join(tmp, "gale.lock")
	lf := &lockfile.LockFile{
		Packages: map[string]lockfile.LockedPackage{
			"git": {
				Version:        "2.53.0",
				SHA256:         "aaa",
				ManifestDigest: testManifestDigest,
			},
		},
	}
	if err := lockfile.Write(lockPath, lf); err != nil {
		t.Fatal(err)
	}

	// Cached install (sha256 empty) resolving to the canonical
	// revision form triggers the relock rewrite.
	ctx := &cmdContext{GalePath: configPath}
	if err := ctx.WriteConfigAndLock(
		"git", "2.53.0", "2.53.0-2", "", "",
	); err != nil {
		t.Fatalf("WriteConfigAndLock: %v", err)
	}

	got, err := lockfile.Read(lockPath)
	if err != nil {
		t.Fatalf("lockfile.Read: %v", err)
	}
	entry, ok := got.Packages["git"]
	if !ok {
		t.Fatal("git entry missing after relock")
	}
	if entry.Version != "2.53.0-2" {
		t.Errorf("Version = %q, want %q", entry.Version, "2.53.0-2")
	}
	if entry.SHA256 != "aaa" {
		t.Errorf("SHA256 = %q, want %q (hash must carry forward)",
			entry.SHA256, "aaa")
	}
	if entry.ManifestDigest != testManifestDigest {
		t.Errorf("ManifestDigest = %q, want %q (digest must survive "+
			"the bare-to-canonical relock)",
			entry.ManifestDigest, testManifestDigest)
	}
}

// v1LockFixture is a minimal but complete v1 lockfile, guard
// included. It exists to pin what the legacy writers do when they
// meet one.
const v1LockFixture = `version = 1

[packages."!gale-lock-v1"]
version = 1

[targets.default]
roots = ["pkg@1.0.0-1"]

[packages."pkg@1.0.0-1".artifacts."darwin/arm64"]
sha256 = "aaaa"
method = "binary"
graph_digest = "sha256:cccc"
`

// TestUpdateLockfileRefusesToRewriteV1 pins why the legacy writers
// may keep reading the flat schema while the readers learn both: the
// downgrade guard makes a v1 file undecodable as flat, so a legacy
// write-back fails instead of replacing an enforced lock with an
// advisory one. The file must come back byte-identical.
//
// This is the guard doing the job it was added for, from inside the
// same binary rather than from an older release. When the v1 writers
// land the refusal is replaced by a real update; until then a silent
// success here would be a lock quietly downgraded.
func TestUpdateLockfileRefusesToRewriteV1(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "gale.lock")
	if err := os.WriteFile(lockPath, []byte(v1LockFixture), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	if err := updateLockfile(
		lockPath, "pkg", "1.0.0-1", "deadbeef", "",
	); err == nil {
		t.Error("updateLockfile rewrote a v1 lockfile, want refusal")
	}

	got, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != v1LockFixture {
		t.Errorf("v1 lockfile was modified:\n%s", got)
	}
}

// TestUpdateLockfileRefusesDanglingSymlink closes the writer half of
// the fail-open the readers just closed. The legacy reader reported a
// gale.lock symlink with a missing target as absent, so the writer
// derived a fresh document from an empty one and atomicfile.Write's
// os.Rename replaced the symlink itself with a regular file.
//
// The symlink is a user artifact — a chezmoi-managed link into a shared
// lock is the obvious case — and replacing it silently loses it. The
// write must refuse, the link must survive, and the missing target must
// stay missing.
func TestUpdateLockfileRefusesDanglingSymlink(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "gale.lock")
	target := filepath.Join(dir, "shared.lock")
	if err := os.Symlink(target, lockPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if err := updateLockfile(
		lockPath, "pkg", "1.0.0-1", "deadbeef", "",
	); err == nil {
		t.Error("updateLockfile wrote through a dangling symlink, want refusal")
	}

	fi, err := os.Lstat(lockPath)
	if err != nil {
		t.Fatalf("lock path is gone: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("lock path is now a %s, want the symlink preserved", fi.Mode())
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Errorf("symlink target was created: %v", err)
	}
}
