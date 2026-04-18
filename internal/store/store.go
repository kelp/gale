package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create store directory: %w", err)
	}
	return dir, nil
}

// StorePath returns the absolute path to the store dir for
// (name, version) if one exists, with fallback to the bare
// version dir for "-1" suffixed requests. Returns ("", false)
// if no directory exists.
func (s *Store) StorePath(name, version string) (string, bool) {
	resolved, ok := s.resolveVersion(name, version)
	if !ok {
		return "", false
	}
	return filepath.Join(s.Root, name, resolved), true
}

// resolveVersion returns the actual version directory name to use for
// name+version, along with whether that directory exists.
//
// Resolution order:
//  1. A bare version (no "-") prefers the canonical "<v>-1" dir if
//     present — that's where new installs land.
//  2. Exact match on the requested version.
//  3. A "<v>-1" suffix falls back to a bare "<v>" dir — that's where
//     pre-revision installs live.
//
// Steps 1 and 3 together let pre-revision configs (bare versions) find
// freshly-migrated installs, and revision-aware configs ("<v>-1") find
// legacy installs, without forcing a hard filesystem migration.
func (s *Store) resolveVersion(name, version string) (string, bool) {
	if !strings.Contains(version, "-") {
		canonical := version + "-1"
		canonicalDir := filepath.Join(s.Root, name, canonical)
		if _, err := os.Stat(canonicalDir); err == nil {
			return canonical, true
		}
	}
	dir := filepath.Join(s.Root, name, version)
	if _, err := os.Stat(dir); err == nil {
		return version, true
	}
	if strings.HasSuffix(version, "-1") {
		bare := strings.TrimSuffix(version, "-1")
		bareDir := filepath.Join(s.Root, name, bare)
		if _, err := os.Stat(bareDir); err == nil {
			return bare, true
		}
	}
	return version, false
}

// IsInstalled checks if a package version exists in the store.
// Returns false for empty directories left by failed installs.
// When version ends with "-1" and the exact directory is absent,
// falls back to the bare version directory (back-compat).
func (s *Store) IsInstalled(name, version string) bool {
	resolved, ok := s.resolveVersion(name, version)
	if !ok {
		return false
	}
	dir := filepath.Join(s.Root, name, resolved)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	return len(entries) > 0
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
			// Skip in-progress build temp dirs from
			// InstallLocal (same-filesystem staging).
			if strings.HasPrefix(versionEntry.Name(), ".build-") {
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
// When version ends with "-1" and the exact directory is absent,
// falls back to the bare version directory (back-compat).
func (s *Store) Remove(name, version string) error {
	resolved, ok := s.resolveVersion(name, version)
	if !ok {
		return fmt.Errorf("remove %s@%s: %w", name, version, ErrNotInstalled)
	}
	dir := filepath.Join(s.Root, name, resolved)
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
