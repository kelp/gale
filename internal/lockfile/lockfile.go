package lockfile

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// LockFile represents a gale.lock file.
type LockFile struct {
	Packages map[string]string `toml:"packages"`
}

// Read reads a gale.lock file. Returns empty LockFile
// if the file doesn't exist.
func Read(path string) (*LockFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &LockFile{
				Packages: make(map[string]string),
			}, nil
		}
		return nil, fmt.Errorf("reading lock file: %w", err)
	}

	var lf LockFile
	if _, err := toml.Decode(string(data), &lf); err != nil {
		return nil, fmt.Errorf("parsing lock file: %w", err)
	}
	if lf.Packages == nil {
		lf.Packages = make(map[string]string)
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

	tmp, err := os.CreateTemp(filepath.Dir(path), ".gale.lock.*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("syncing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("closing temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("renaming temp file: %w", err)
	}
	return nil
}

// IsStale checks if the lock file is stale relative to
// the gale.toml packages. Returns true if packages differ
// or if gale.toml is newer than the lock file.
func IsStale(
	galeTOMLPath, lockPath string,
	tomlPackages map[string]string,
) (bool, error) {
	lockInfo, err := os.Stat(lockPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		return false, fmt.Errorf("stat lock file: %w", err)
	}

	tomlInfo, err := os.Stat(galeTOMLPath)
	if err != nil {
		return false, fmt.Errorf("stat gale.toml: %w", err)
	}

	if tomlInfo.ModTime().After(lockInfo.ModTime()) {
		return true, nil
	}

	lf, err := Read(lockPath)
	if err != nil {
		return false, fmt.Errorf("reading lock file: %w", err)
	}

	if len(tomlPackages) != len(lf.Packages) {
		return true, nil
	}
	for name, version := range tomlPackages {
		lockVersion, ok := lf.Packages[name]
		if !ok || lockVersion != version {
			return true, nil
		}
	}

	return false, nil
}
