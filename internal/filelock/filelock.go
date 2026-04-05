package filelock

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// With acquires an exclusive lock on path, runs fn,
// and releases the lock. The lock file is created if
// needed and kept on disk (never deleted).
func With(path string, fn func() error) error {
	unlock, err := Acquire(path)
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}

// Acquire acquires an exclusive lock on path. Returns
// an unlock function. Caller must defer unlock().
// The lock file is kept on disk after unlock.
func Acquire(path string) (unlock func(), err error) {
	// Ensure parent directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("filelock: %w", err)
	}

	// Create or open lock file
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("filelock: %w", err)
	}

	// Acquire exclusive lock
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil { //nolint:gosec // fd fits int on all supported platforms
		f.Close()
		return nil, fmt.Errorf("filelock: %w", err)
	}

	// Return unlock function
	unlock = func() {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN) //nolint:gosec // fd fits int on all supported platforms
		f.Close()
	}
	return unlock, nil
}
