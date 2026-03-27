package generation

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// Build creates a new generation from the package map,
// atomically swaps the current symlink, and cleans up
// the previous generation.
func Build(pkgs map[string]string, galeDir, storeRoot string) error {
	prev, err := Current(galeDir)
	if err != nil {
		return fmt.Errorf("read current generation: %w", err)
	}

	next := prev + 1

	genDir := filepath.Join(
		galeDir, "generations", strconv.Itoa(next))
	genBinDir := filepath.Join(genDir, "bin")
	if err := os.MkdirAll(genBinDir, 0o755); err != nil {
		return fmt.Errorf("create generation bin dir: %w", err)
	}

	// Clean up the new generation directory on any
	// subsequent error so we don't leave orphaned dirs.
	cleanup := func() { os.RemoveAll(genDir) }

	for name, version := range pkgs {
		pkgBinDir := filepath.Join(
			storeRoot, name, version, "bin")
		entries, err := os.ReadDir(pkgBinDir)
		if err != nil {
			cleanup()
			return fmt.Errorf(
				"read store bin dir for %s: %w", name, err)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			target := filepath.Join(pkgBinDir, entry.Name())
			linkPath := filepath.Join(genBinDir, entry.Name())
			if err := os.Symlink(target, linkPath); err != nil {
				cleanup()
				return fmt.Errorf(
					"symlink %s: %w", entry.Name(), err)
			}
		}
	}

	// Atomic swap: create a temporary symlink then rename.
	relTarget := filepath.Join(
		"generations", strconv.Itoa(next))
	tmpLink := filepath.Join(galeDir, "current-new")
	if err := os.Remove(tmpLink); err != nil && !errors.Is(err, os.ErrNotExist) {
		cleanup()
		return fmt.Errorf("remove stale temp link: %w", err)
	}
	if err := os.Symlink(relTarget, tmpLink); err != nil {
		cleanup()
		return fmt.Errorf("create temp current symlink: %w", err)
	}
	if err := os.Rename(tmpLink, filepath.Join(galeDir, "current")); err != nil {
		cleanup()
		return fmt.Errorf("atomic swap current symlink: %w", err)
	}

	// Clean up previous generation.
	if prev > 0 {
		prevDir := filepath.Join(
			galeDir, "generations", strconv.Itoa(prev))
		if err := os.RemoveAll(prevDir); err != nil {
			return fmt.Errorf(
				"remove previous generation: %w", err)
		}
	}

	return nil
}

// Current returns the active generation number by
// resolving the current symlink. Returns 0 if no
// current generation exists.
func Current(galeDir string) (int, error) {
	currentPath := filepath.Join(galeDir, "current")
	target, err := os.Readlink(currentPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("read current symlink: %w", err)
	}

	numStr := filepath.Base(target)
	n, err := strconv.Atoi(numStr)
	if err != nil {
		return 0, fmt.Errorf(
			"parse generation number %q: %w", numStr, err)
	}
	return n, nil
}

// Next returns the next generation number (Current+1,
// or 1 if none exists).
func Next(galeDir string) (int, error) {
	cur, err := Current(galeDir)
	if err != nil {
		return 0, err
	}
	return cur + 1, nil
}
