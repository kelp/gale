package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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
//  1. A bare version (no "-") returns the highest "<v>-<N>" on disk.
//     This matches the CLAUDE.md contract "a bare @version resolves
//     to the highest revision known", so a recipe's revision bump
//     starts flowing through config/lookup without needing users to
//     re-pin revision numbers in gale.toml.
//  2. Exact match on the requested version.
//  3. A "<v>-1" suffix falls back to a bare "<v>" dir — that's where
//     pre-revision installs live.
//  4. A bare version falls back to the bare dir itself (legacy
//     pre-revision installs) if no "<v>-<N>" dirs exist.
//
// Implementation: a single os.ReadDir on <root>/<name>/ answers every
// question above from one atomic directory listing. The previous
// implementation chained up to three os.Stat calls plus a ReadDir,
// which raced with a concurrent `gale gc` removing a sibling
// mid-chain (M2 in TODO.md).
func (s *Store) resolveVersion(name, version string) (string, bool) {
	entries, err := os.ReadDir(filepath.Join(s.Root, name))
	if err != nil {
		return version, false
	}
	return resolveVersionFromEntries(entries, version)
}

// resolveVersionFromEntries applies the resolution order documented
// on resolveVersion to a directory listing of <root>/<name>/.
// Broken out so callers that already hold the listing (e.g. future
// batch lookups) can reuse the logic without a redundant syscall.
func resolveVersionFromEntries(entries []os.DirEntry, version string) (string, bool) {
	hasDash := strings.Contains(version, "-")
	prefix := version + "-"

	hasExact := false
	hasBareForDashOne := false
	var bare string
	if strings.HasSuffix(version, "-1") {
		bare = strings.TrimSuffix(version, "-1")
	}

	bestRev := -1
	bestName := ""

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasPrefix(n, ".build-") {
			continue
		}
		if n == version {
			hasExact = true
		}
		if bare != "" && n == bare {
			hasBareForDashOne = true
		}
		if !hasDash && strings.HasPrefix(n, prefix) {
			rev, err := strconv.Atoi(n[len(prefix):])
			if err == nil && rev >= 0 && rev > bestRev {
				bestRev = rev
				bestName = n
			}
		}
	}

	// Bare request: highest "<v>-<N>" wins over bare "<v>".
	if !hasDash && bestRev >= 0 {
		return bestName, true
	}
	// Exact match.
	if hasExact {
		return version, true
	}
	// "-1" fallback to bare "<v>".
	if hasBareForDashOne {
		return bare, true
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

// Remove removes a package version from the store. Prefers
// exact match — callers that pass an on-disk name (e.g. from
// List) must get exactly that directory removed. Falls back
// to the back-compat patterns (bare dir for a "-1" request,
// highest-revision for a bare request) only when no exact
// match exists, so user-facing commands like
// `gale remove jq@1.8.1` still work regardless of which
// revision layout the store actually holds.
func (s *Store) Remove(name, version string) error {
	exact := filepath.Join(s.Root, name, version)
	if _, err := os.Stat(exact); err == nil {
		if err := os.RemoveAll(exact); err != nil &&
			!errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove version directory: %w", err)
		}
		return cleanupEmptyNameDir(s.Root, name)
	}

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

	return cleanupEmptyNameDir(s.Root, name)
}

// cleanupEmptyNameDir removes the parent <root>/<name> dir
// if no version dirs remain. Missing parent is not an error.
func cleanupEmptyNameDir(root, name string) error {
	nameDir := filepath.Join(root, name)
	entries, err := os.ReadDir(nameDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read name directory: %w", err)
	}
	if len(entries) == 0 {
		if err := os.Remove(nameDir); err != nil &&
			!errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf(
				"remove empty name directory: %w", err)
		}
	}
	return nil
}
