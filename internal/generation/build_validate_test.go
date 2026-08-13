package generation

import (
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kelp/gale/internal/farm"
	"github.com/kelp/gale/internal/filelock"
)

// --- P8: generation validation callback (design section 6, acceptance 12) ---
//
// BuildWithValidate lets a caller (P7's locked sync) revalidate every
// plan entry immediately before the generation is built and swapped,
// closing the TOCTOU window between per-artifact verification and
// activation. This test pins the contract end to end: the callback
// runs before anything is mutated, it runs under the SAME store-gen
// lock acquisition as the rest of the build (not a second one that
// would reopen the window), and a callback error leaves the
// generation directory, the current symlink, and the farm untouched.

func TestBuildWithValidate_CallbackErrorMutatesNothing(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := t.TempDir()
	createStoreEntry(t, storeRoot, "jq", "1.8.1", []string{"jq"})
	pkgs := map[string]string{"jq": "1.8.1"}

	sentinel := errors.New("lock-integrity error: drift detected after per-artifact verification")

	inCallback := make(chan struct{})
	release := make(chan struct{})
	var callbackCalls int32

	validate := func() error {
		atomic.AddInt32(&callbackCalls, 1)
		close(inCallback)
		<-release
		return sentinel
	}

	buildErr := make(chan error, 1)
	go func() {
		buildErr <- BuildWithValidate(pkgs, galeDir, storeRoot, validate)
	}()

	// Wait for the callback to start running before touching anything
	// else — proves the callback fires before Build does.
	<-inCallback

	// While the callback is mid-flight (and hasn't returned), a second
	// attempt to acquire the same store-gen lock must block. If it
	// didn't, the callback would be running under a DIFFERENT
	// acquisition than the generation build/swap that follows it,
	// which is exactly the TOCTOU gap section 6 exists to close.
	lockPath := filepath.Join(filepath.Dir(storeRoot), "generation.lock")
	// Buffered, and the error is returned rather than reported from the
	// goroutine: an unbuffered send would block this goroutine forever
	// once the select below stops listening, leaking it while it holds
	// the lock, and t.Errorf from a goroutine that outlives the test
	// panics instead of failing it.
	gotLock := make(chan func(), 1)
	acquireErr := make(chan error, 1)
	go func() {
		unlock, err := filelock.Acquire(lockPath)
		if err != nil {
			acquireErr <- err
			return
		}
		gotLock <- unlock
	}()

	select {
	case unlock := <-gotLock:
		unlock()
		t.Fatal("contending lock was acquired while the validate callback was still running")
	case <-time.After(100 * time.Millisecond):
		// Expected: the contending acquire is still blocked.
	}

	close(release)

	if err := <-buildErr; !errors.Is(err, sentinel) {
		t.Fatalf("BuildWithValidate error = %v, want sentinel %v", err, sentinel)
	}
	if got := atomic.LoadInt32(&callbackCalls); got != 1 {
		t.Fatalf("validate called %d times, want 1", got)
	}

	// No generation directory left behind.
	if _, err := os.Stat(filepath.Join(galeDir, "gen", "1")); !os.IsNotExist(err) {
		t.Errorf("gen/1 should not exist after a callback error, err=%v", err)
	}

	// No current symlink created.
	if _, err := os.Lstat(filepath.Join(galeDir, "current")); !os.IsNotExist(err) {
		t.Errorf("current symlink should not exist after a callback error, err=%v", err)
	}

	// No farm mutation: farm.Rebuild is only reached after the
	// generation swap, so the farm dir must never even be created.
	if _, err := os.Stat(
		farm.DirFromStoreRoot(storeRoot),
	); !os.IsNotExist(err) {
		t.Errorf("farm dir should not exist after a callback error, err=%v", err)
	}

	// The lock must be released once BuildWithValidate returns, so
	// the earlier contender can now acquire it. The contender is
	// parked in flock, so its wakeup is asynchronous and the wait
	// needs a clock; deadlockBackstop makes it a deadlock backstop
	// rather than a timing margin (gh#251).
	select {
	case unlock := <-gotLock:
		unlock()
	case err := <-acquireErr:
		t.Fatalf("acquire contending lock: %v", err)
	case <-time.After(deadlockBackstop):
		t.Fatal("store-gen lock was not released after BuildWithValidate returned")
	}
}

// TestBuildWithValidate_NilCallbackMatchesBuild pins that Build is
// exactly BuildWithValidate with a nil callback — the "nil path is
// identical" half of P8's exit criteria — by driving both through the
// same scenario and comparing the resulting generation layout.
func TestBuildWithValidate_NilCallbackMatchesBuild(t *testing.T) {
	pkgs := map[string]string{"jq": "1.8.1"}

	runViaBuild := t.TempDir()
	storeRootA := t.TempDir()
	createStoreEntry(t, storeRootA, "jq", "1.8.1", []string{"jq"})
	if err := Build(pkgs, runViaBuild, storeRootA); err != nil {
		t.Fatalf("Build error: %v", err)
	}

	runViaValidate := t.TempDir()
	storeRootB := t.TempDir()
	createStoreEntry(t, storeRootB, "jq", "1.8.1", []string{"jq"})
	if err := BuildWithValidate(pkgs, runViaValidate, storeRootB, nil); err != nil {
		t.Fatalf("BuildWithValidate(nil) error: %v", err)
	}

	for _, name := range []string{"gen/1/bin/jq", "current"} {
		aInfo, aErr := os.Lstat(filepath.Join(runViaBuild, name))
		bInfo, bErr := os.Lstat(filepath.Join(runViaValidate, name))
		if aErr != nil || bErr != nil {
			t.Fatalf("lstat %s: Build err=%v, BuildWithValidate(nil) err=%v", name, aErr, bErr)
		}
		if aInfo.Mode()&os.ModeSymlink != bInfo.Mode()&os.ModeSymlink {
			t.Errorf("%s: symlink-ness differs between Build and BuildWithValidate(nil)", name)
		}
	}
}
