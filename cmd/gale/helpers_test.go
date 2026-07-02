package main

import (
	"io"
	"os"
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
