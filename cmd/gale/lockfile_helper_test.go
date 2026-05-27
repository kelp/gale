package main

// Tests for updateLockfile, the helper that runSyncOne calls to
// persist SHA256 hashes after a successful install.
//
// These tests exercise existing production code (not stubs) and
// are expected to PASS.

import (
	"os"
	"path/filepath"
	"testing"
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
	err := updateLockfile(lockPath, "pkg", "1.0.0", "deadbeef")
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

	err := updateLockfile(lockPath, "pkg", "1.0.0", "deadbeef")
	if err == nil {
		t.Error("updateLockfile returned nil, want non-nil error " +
			"when the parent directory is not writable")
	}
}
