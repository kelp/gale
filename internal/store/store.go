package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrNotInstalled is returned when removing a package that does not exist.
var ErrNotInstalled = errors.New("package not installed")

// InstalledPackage represents a package in the store.
type InstalledPackage struct {
	Name    string
	Version string
}

// Store manages the package store directory.
type Store struct {
	Root string
}

// NewStore creates a Store with the given root directory.
func NewStore(root string) *Store {
	return &Store{Root: root}
}

// Create creates the directory for a package version.
// It is idempotent; calling it for an existing version succeeds.
// Returns the full path to the version directory.
func (s *Store) Create(name, version string) (string, error) {
	dir := filepath.Join(s.Root, name, version)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create store directory: %w", err)
	}
	return dir, nil
}

// IsInstalled checks if a package version exists in the store.
func (s *Store) IsInstalled(name, version string) bool {
	dir := filepath.Join(s.Root, name, version)
	info, err := os.Stat(dir)
	if err != nil {
		return false
	}
	return info.IsDir()
}

// List returns all installed packages in the store.
func (s *Store) List() ([]InstalledPackage, error) {
	nameEntries, err := os.ReadDir(s.Root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("list store root: %w", err)
	}

	var pkgs []InstalledPackage
	for _, nameEntry := range nameEntries {
		if !nameEntry.IsDir() {
			continue
		}
		versionEntries, err := os.ReadDir(
			filepath.Join(s.Root, nameEntry.Name()),
		)
		if err != nil {
			return nil, fmt.Errorf("list versions for %s: %w",
				nameEntry.Name(), err)
		}
		for _, versionEntry := range versionEntries {
			if !versionEntry.IsDir() {
				continue
			}
			pkgs = append(pkgs, InstalledPackage{
				Name:    nameEntry.Name(),
				Version: versionEntry.Name(),
			})
		}
	}
	return pkgs, nil
}

// Remove removes a package version from the store.
func (s *Store) Remove(name, version string) error {
	dir := filepath.Join(s.Root, name, version)
	if _, err := os.Stat(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s@%s: %w",
				name, version, ErrNotInstalled)
		}
		return fmt.Errorf("stat version directory: %w", err)
	}

	if err := os.RemoveAll(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove version directory: %w", err)
	}

	// Clean up empty parent name directory.
	nameDir := filepath.Join(s.Root, name)
	entries, err := os.ReadDir(nameDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read name directory: %w", err)
	}
	if len(entries) == 0 {
		if err := os.Remove(nameDir); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove empty name directory: %w", err)
		}
	}

	return nil
}
