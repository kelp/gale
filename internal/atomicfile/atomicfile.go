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
func Write(path string, data []byte) error {
	// Create parent directories if needed
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("atomicfile: %w", err)
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

	// Atomic rename
	if writeErr = os.Rename(tmpName, path); writeErr != nil {
		return fmt.Errorf("atomicfile: %w", writeErr)
	}

	return nil
}
