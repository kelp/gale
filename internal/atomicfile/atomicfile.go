package atomicfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// ErrSymlink reports a symlink at the path a write named. Callers
// match it to tell a user-planted link apart from an I/O failure.
var ErrSymlink = errors.New("a symlink, not a regular file")

// Write atomically replaces path with data.
// Creates parent directories if needed.
// Uses temp file in same directory + fsync + rename.
// Cleans up temp file on any error.
// Preserves the existing file's permission bits when the target
// already exists. New files are created with mode 0o644 (world-
// readable), matching the convention used throughout gale for
// config files, lock files, and generated README files.
//
// Refuses with ErrSymlink when path is itself a symlink: the rename
// would replace the link with a regular file and strand its target
// (gh#193). Only the final component is examined, so a path reached
// through a symlinked parent still writes.
//
// The check is file preservation, not a security boundary. A link
// planted between the check and the rename still wins; closing that
// race needs RENAME_NOREPLACE, which is Linux-only and forbids the
// overwrite this function exists to perform.
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
	//
	// Lstat, not Stat: Stat follows a link at the final component,
	// which both loses the mode of the entry being replaced and
	// reads a planted link as an ordinary file.
	targetMode := os.FileMode(0o644) //nolint:gosec
	fi, err := os.Lstat(path)
	switch {
	case err == nil && fi.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf(
			"%s: %w: replace it with a regular file for gale to write it",
			path, ErrSymlink,
		)
	case err == nil && fi.Mode().IsRegular():
		targetMode = fi.Mode().Perm()
	case err == nil:
		// Some other entry type. Leave it to the rename, as before.
	case errors.Is(err, fs.ErrNotExist):
		// A new file, written at the 0o644 default.
	default:
		// The question was not answered — EACCES, or ELOOP from a
		// symlink cycle in an ancestor. An unanswered question must
		// not read as "nothing is there", the same rule the lockfile
		// and config readers follow.
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
