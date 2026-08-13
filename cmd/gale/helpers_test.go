package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

// chdirTo changes the working directory to dir for the
// duration of the test and restores it on cleanup.
func chdirTo(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

// captureStderr runs fn and returns everything written to
// os.Stderr during the call. os.Stderr is restored to its
// original value before captureStderr returns.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStderr := os.Stderr
	// Restore immediately on return, not just at test end,
	// so os.Stderr is valid for the rest of the test body.
	defer func() {
		if os.Stderr == w {
			os.Stderr = origStderr
		}
	}()
	// Fallback: ensure restore even if fn panics and the
	// deferred restore above runs before t.Cleanup.
	t.Cleanup(func() { os.Stderr = origStderr })

	os.Stderr = w

	ch := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(r)
		ch <- string(data)
	}()

	fn()
	w.Close()
	captured := <-ch
	r.Close()
	return captured
}

// breakGaleDir points HOME at a fresh directory and plants a
// regular file where ~/.gale has to be a directory, so every
// MkdirAll beneath it fails with ENOTDIR. Denying access with
// chmod proves nothing in the agent container, which runs as root
// (docs/dev/agent-environment.md); a structurally impossible path
// fails for root too.
func breakGaleDir(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	blocker := filepath.Join(home, ".gale")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatalf("plant %s as a regular file: %v", blocker, err)
	}
}

// breakSystemTemp points TMPDIR at a path underneath a regular
// file, so os.TempDir() names a location nothing — root included
// — can create. Together with breakGaleDir this is what makes
// build.TmpDir fail rather than fall back (gh#235).
//
// The scratch dir it needs is allocated before TMPDIR moves,
// since t.TempDir() resolves through os.TempDir() itself.
func breakSystemTemp(t *testing.T) {
	t.Helper()
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatalf("plant %s as a regular file: %v", blocker, err)
	}
	t.Setenv("TMPDIR", filepath.Join(blocker, "tmp"))
}
