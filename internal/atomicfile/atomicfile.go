package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// Write atomically replaces path with data.
// Creates parent directories if needed.
// Uses temp file in same directory + fsync + rename.
// Cleans up temp file on any error.
// Preserves the existing file's permission bits when the target
// already exists. New files are created with mode 0o644 (world-
// readable), matching the convention used throughout gale for
// config files, lock files, and generated README files.
func Write(path string, data []byte) error {
	// Create parent directories if needed
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("atomicfile: %w", err)
	}

	// Determine the permission bits to apply before rename.
	// If the target already exists, preserve its mode so the rename
	// does not silently downgrade permissions (e.g. 0644 → 0600).
	// os.CreateTemp produces 0600; we chmod after writing and before
	// renaming. New files default to 0644 (world-readable) per the
	// project convention; gosec G306 is a false positive here.
	targetMode := os.FileMode(0o644) //nolint:gosec
	if fi, err := os.Stat(path); err == nil {
		targetMode = fi.Mode().Perm()
	}

	// Create temp file in same directory
	tmp, err := os.CreateTemp(dir, ".gale-tmp-*")
	if err != nil {
		return fmt.Errorf("atomicfile: %w", err)
	}
	tmpName := tmp.Name()

	// Clean up temp file on any error
	var writeErr error
	defer func() {
		if writeErr != nil {
			os.Remove(tmpName)
		}
	}()

	// Write data
	if _, writeErr = tmp.Write(data); writeErr != nil {
		tmp.Close()
		return fmt.Errorf("atomicfile: %w", writeErr)
	}

	// Sync to disk
	if writeErr = tmp.Sync(); writeErr != nil {
		tmp.Close()
		return fmt.Errorf("atomicfile: %w", writeErr)
	}

	// Close temp file
	if writeErr = tmp.Close(); writeErr != nil {
		return fmt.Errorf("atomicfile: %w", writeErr)
	}

	// Apply the target's permissions to the temp file before the
	// rename so the final file is never transiently visible with
	// the wrong mode.
	if writeErr = os.Chmod(tmpName, targetMode); writeErr != nil {
		return fmt.Errorf("atomicfile: %w", writeErr)
	}

	// Atomic rename
	if writeErr = os.Rename(tmpName, path); writeErr != nil {
		return fmt.Errorf("atomicfile: %w", writeErr)
	}

	return nil
}
