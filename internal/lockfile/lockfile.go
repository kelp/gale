package lockfile

import (
	"bytes"
	"errors"
	"fmt"
	"os"

	"github.com/BurntSushi/toml"

	"github.com/kelp/gale/internal/atomicfile"
)

// LockedPackage represents a pinned package in the lockfile.
type LockedPackage struct {
	Version string `toml:"version"`
	SHA256  string `toml:"sha256,omitempty"`
}

// LockFile represents a gale.lock file.
type LockFile struct {
	Packages map[string]LockedPackage `toml:"packages"`
}

// Read reads a gale.lock file. Returns empty LockFile
// if the file doesn't exist.
func Read(path string) (*LockFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &LockFile{
				Packages: make(map[string]LockedPackage),
			}, nil
		}
		return nil, fmt.Errorf("reading lock file: %w", err)
	}

	var lf LockFile
	if _, err := toml.Decode(string(data), &lf); err != nil {
		return nil, fmt.Errorf("parsing lock file: %w", err)
	}
	if lf.Packages == nil {
		lf.Packages = make(map[string]LockedPackage)
	}
	return &lf, nil
}

// Write writes a LockFile to the given path atomically.
func Write(path string, lf *LockFile) error {
	var buf bytes.Buffer
	enc := toml.NewEncoder(&buf)
	if err := enc.Encode(lf); err != nil {
		return fmt.Errorf("encoding lock file: %w", err)
	}
	return atomicfile.Write(path, buf.Bytes())
}

// IsStale checks if the lock file is stale relative to
// the gale.toml packages. Returns true if packages differ
// or if gale.toml is newer than the lock file.
func IsStale(
	galeTOMLPath, lockPath string,
	tomlPackages map[string]string,
) (bool, error) {
	_, err := os.Stat(lockPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		return false, fmt.Errorf("stat lock file: %w", err)
	}

	if _, err := os.Stat(galeTOMLPath); err != nil {
		return false, fmt.Errorf("stat gale.toml: %w", err)
	}

	// Always compare package content rather than relying
	// on mtime, which can be misleading under clock skew.
	lf, err := Read(lockPath)
	if err != nil {
		return false, fmt.Errorf("reading lock file: %w", err)
	}

	if len(tomlPackages) != len(lf.Packages) {
		return true, nil
	}
	for name, version := range tomlPackages {
		locked, ok := lf.Packages[name]
		if !ok || locked.Version != version {
			return true, nil
		}
	}

	return false, nil
}
