package filelock

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
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

	if err != expectedErr {
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

	// Second call should succeed (lock was released)
	done := make(chan struct{})
	go func() {
		err := With(lockPath, func() error {
			return nil
		})
		if err != nil {
			t.Errorf("second With() error: %v", err)
		}
		close(done)
	}()

	select {
	case <-done:
		// Success - lock was released
	case <-time.After(1 * time.Second):
		t.Fatal("deadlock: second With() did not acquire lock after panic")
	}
}
