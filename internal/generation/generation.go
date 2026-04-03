package generation

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

//go:embed gale-readme.md
var galeReadme []byte

// Build creates a new generation from the package map
// and atomically swaps the current symlink. Previous
// generations are retained for history and rollback.
func Build(pkgs map[string]string, galeDir, storeRoot string) error {
	prev, err := Current(galeDir)
	if err != nil {
		return fmt.Errorf("read current generation: %w", err)
	}

	next := prev + 1

	genDir := filepath.Join(
		galeDir, "gen", strconv.Itoa(next))

	// Always create bin/ — it's the minimum required
	// directory (user adds it to PATH).
	if err := os.MkdirAll(
		filepath.Join(genDir, "bin"), 0o755); err != nil {
		return fmt.Errorf("create generation dir: %w", err)
	}

	// Clean up the new generation directory on any
	// subsequent error so we don't leave orphaned dirs.
	cleanup := func() { os.RemoveAll(genDir) }

	// Symlink contents from each package's store entry
	// into the generation directory. Scan every
	// subdirectory in the package — don't hardcode names.
	for name, version := range pkgs {
		pkgDir := filepath.Join(storeRoot, name, version)
		entries, err := os.ReadDir(pkgDir)
		if err != nil {
			cleanup()
			return fmt.Errorf("read store %s: %w", name, err)
		}
		for _, e := range entries {
			if e.IsDir() {
				srcDir := filepath.Join(pkgDir, e.Name())
				dstDir := filepath.Join(genDir, e.Name())
				if err := os.MkdirAll(dstDir, 0o755); err != nil {
					cleanup()
					return fmt.Errorf(
						"create gen %s dir: %w", e.Name(), err)
				}
				if err := symlinkDir(srcDir, dstDir); err != nil {
					cleanup()
					return fmt.Errorf(
						"symlink %s/%s: %w", name, e.Name(), err)
				}
				continue
			}

			// Symlink root-level files (e.g., go.env).
			// Skip if already present from another package.
			src := filepath.Join(pkgDir, e.Name())
			dst := filepath.Join(genDir, e.Name())
			if _, err := os.Lstat(dst); err == nil {
				continue
			}
			if err := os.Symlink(src, dst); err != nil {
				cleanup()
				return fmt.Errorf(
					"symlink %s/%s: %w", name, e.Name(), err)
			}
		}
	}

	// Atomic swap: create a temporary symlink then rename.
	relTarget := filepath.Join(
		"gen", strconv.Itoa(next))
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

	// Write README (best effort, world-readable).
	_ = os.WriteFile(
		filepath.Join(galeDir, "README.md"),
		galeReadme, 0o644)

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

// symlinkDir creates symlinks in dstDir for every file
// in srcDir. Skips if srcDir doesn't exist. Recursively
// handles subdirectories (e.g., man/man1/).
func symlinkDir(srcDir, dstDir string) error {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // package doesn't have this dir
		}
		return err
	}

	for _, entry := range entries {
		src := filepath.Join(srcDir, entry.Name())
		dst := filepath.Join(dstDir, entry.Name())

		if entry.IsDir() {
			// Recurse into subdirectories (e.g., man/man1/).
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
			if err := symlinkDir(src, dst); err != nil {
				return err
			}
			continue
		}

		// Skip if a symlink already exists (another
		// package provides the same file).
		if _, err := os.Lstat(dst); err == nil {
			continue
		}

		if err := os.Symlink(src, dst); err != nil {
			return err
		}
	}
	return nil
}
