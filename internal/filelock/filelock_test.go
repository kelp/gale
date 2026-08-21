package filelock

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestWithRunsFnAndReturnsResult(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "test.lock")

	called := false
	err := With(lockPath, func() error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("With() returned error: %v", err)
	}
	if !called {
		t.Fatal("fn was not called")
	}
}

func TestWithReturnsFnError(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "test.lock")

	expectedErr := errors.New("fn error")
	err := With(lockPath, func() error {
		return expectedErr
	})

	if !errors.Is(err, expectedErr) {
		t.Fatalf("With() returned %v, want %v", err, expectedErr)
	}
}

func TestWithCreatesLockFile(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "test.lock")

	err := With(lockPath, func() error {
		return nil
	})
	if err != nil {
		t.Fatalf("With() returned error: %v", err)
	}

	if _, err := os.Stat(lockPath); os.IsNotExist(err) {
		t.Fatal("lock file was not created")
	}
}

func TestAcquireAndUnlock(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "test.lock")

	unlock, err := Acquire(lockPath)
	if err != nil {
		t.Fatalf("Acquire() returned error: %v", err)
	}
	if unlock == nil {
		t.Fatal("Acquire() returned nil unlock function")
	}

	// Should not panic
	unlock()
}

func TestLockFilePersistedAfterUnlock(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "test.lock")

	unlock, err := Acquire(lockPath)
	if err != nil {
		t.Fatalf("Acquire() returned error: %v", err)
	}

	unlock()

	if _, err := os.Stat(lockPath); os.IsNotExist(err) {
		t.Fatal("lock file was deleted after unlock")
	}
}

func TestSerializesAccess(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "test.lock")

	var mu sync.Mutex
	var order []int
	ready := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(2)

	// Goroutine 1: acquire, signal, sleep, write
	go func() {
		defer wg.Done()
		err := With(lockPath, func() error {
			close(ready) // Signal that we have the lock
			time.Sleep(50 * time.Millisecond)
			mu.Lock()
			order = append(order, 1)
			mu.Unlock()
			return nil
		})
		if err != nil {
			t.Errorf("goroutine 1 With() error: %v", err)
		}
	}()

	// Goroutine 2: wait for signal, then try to acquire
	go func() {
		defer wg.Done()
		<-ready // Wait for goroutine 1 to acquire lock
		err := With(lockPath, func() error {
			mu.Lock()
			order = append(order, 2)
			mu.Unlock()
			return nil
		})
		if err != nil {
			t.Errorf("goroutine 2 With() error: %v", err)
		}
	}()

	wg.Wait()

	if len(order) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(order))
	}
	if order[0] != 1 || order[1] != 2 {
		t.Fatalf("expected order [1, 2], got %v", order)
	}
}

func TestWithReleasesLockOnPanic(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "test.lock")

	// First call panics
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("expected panic")
			}
		}()
		_ = With(lockPath, func() error {
			panic("test panic")
		})
	}()

	// With's deferred unlock ran while the panic unwound, so
	// the lock is free by now and nothing is pending. Probe it
	// with a non-blocking flock rather than waiting out a
	// deadline: a still-held lock fails right here, and a
	// loaded machine is never mistaken for one (gh#246).
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open lock file: %v", err)
	}
	defer f.Close()
	fd := int(f.Fd()) //nolint:gosec // fd fits int on all supported platforms
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatalf("lock still held after panic: %v", err)
	}
	if err := unix.Flock(fd, unix.LOCK_UN); err != nil {
		t.Fatalf("unlock probe: %v", err)
	}

	// And the released lock is usable again through the API.
	if err := With(lockPath, func() error { return nil }); err != nil {
		t.Errorf("second With() error: %v", err)
	}
}
